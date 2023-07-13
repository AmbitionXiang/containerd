/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/pkg/cri/instrument"
	"github.com/containerd/containerd/pkg/cri/nri"
	"github.com/containerd/containerd/pkg/cri/streaming"
	"github.com/containerd/containerd/pkg/kmutex"
	"github.com/containerd/containerd/plugin"
	cni "github.com/containerd/go-cni"
	"github.com/mdlayher/vsock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/containerd/pkg/cri/store/label"

	criconfig "github.com/containerd/containerd/pkg/cri/config"
	containerstore "github.com/containerd/containerd/pkg/cri/store/container"
	imagestore "github.com/containerd/containerd/pkg/cri/store/image"
	sandboxstore "github.com/containerd/containerd/pkg/cri/store/sandbox"
	snapshotstore "github.com/containerd/containerd/pkg/cri/store/snapshot"
	ctrdutil "github.com/containerd/containerd/pkg/cri/util"
	osinterface "github.com/containerd/containerd/pkg/os"
	"github.com/containerd/containerd/pkg/registrar"
)

// defaultNetworkPlugin is used for the default CNI configuration
const defaultNetworkPlugin = "default"

// CRIService is the interface implement CRI remote service server.
type CRIService interface {
	runtime.RuntimeServiceServer
	runtime.ImageServiceServer
	// Closer is used by containerd to gracefully stop cri service.
	io.Closer

	Run(ready func()) error

	Register(*grpc.Server) error
}

// criService implements CRIService.
type criService struct {
	// config contains all configurations.
	config criconfig.Config
	// imageFSPath is the path to image filesystem.
	imageFSPath string
	// os is an interface for all required os operations.
	os osinterface.OS
	// sandboxStore stores all resources associated with sandboxes.
	sandboxStore *sandboxstore.Store
	// sandboxNameIndex stores all sandbox names and make sure each name
	// is unique.
	sandboxNameIndex *registrar.Registrar
	// containerStore stores all resources associated with containers.
	containerStore *containerstore.Store
	// containerNameIndex stores all container names and make sure each
	// name is unique.
	containerNameIndex *registrar.Registrar
	// imageStore stores all resources associated with images.
	imageStore *imagestore.Store
	// snapshotStore stores information of all snapshots.
	snapshotStore *snapshotstore.Store
	// netPlugin is used to setup and teardown network when run/stop pod sandbox.
	netPlugin map[string]cni.CNI
	// client is an instance of the containerd client
	client *containerd.Client
	// streamServer is the streaming server serves container streaming request.
	streamServer streaming.Server
	// eventMonitor is the monitor monitors containerd events.
	eventMonitor *eventMonitor
	// initialized indicates whether the server is initialized. All GRPC services
	// should return error before the server is initialized.
	initialized atomic.Bool
	// cniNetConfMonitor is used to reload cni network conf if there is
	// any valid fs change events from cni network conf dir.
	cniNetConfMonitor map[string]*cniNetConfSyncer
	// baseOCISpecs contains cached OCI specs loaded via `Runtime.BaseRuntimeSpec`
	baseOCISpecs map[string]*oci.Spec
	// allCaps is the list of the capabilities.
	// When nil, parsed from CapEff of /proc/self/status.
	allCaps []string //nolint:nolintlint,unused // Ignore on non-Linux
	// unpackDuplicationSuppressor is used to make sure that there is only
	// one in-flight fetch request or unpack handler for a given descriptor's
	// or chain ID.
	unpackDuplicationSuppressor kmutex.KeyedLocker
	// nri is used to hook NRI into CRI request processing.
	nri *nri.API
	// containerEventsChan is used to capture container events and send them
	// to the caller of GetContainerEvents.
	containerEventsChan chan runtime.ContainerEventResponse
}

// NewCRIService returns a new instance of CRIService
func NewCRIService(config criconfig.Config, client *containerd.Client, nri *nri.API) (CRIService, error) {
	var err error
	labels := label.NewStore()
	c := &criService{
		config:                      config,
		client:                      client,
		os:                          osinterface.RealOS{},
		sandboxStore:                sandboxstore.NewStore(labels),
		containerStore:              containerstore.NewStore(labels),
		imageStore:                  imagestore.NewStore(client),
		snapshotStore:               snapshotstore.NewStore(),
		sandboxNameIndex:            registrar.NewRegistrar(),
		containerNameIndex:          registrar.NewRegistrar(),
		netPlugin:                   make(map[string]cni.CNI),
		unpackDuplicationSuppressor: kmutex.New(),
	}

	// TODO: figure out a proper channel size.
	c.containerEventsChan = make(chan runtime.ContainerEventResponse, 1000)

	if client.SnapshotService(c.config.ContainerdConfig.Snapshotter) == nil {
		return nil, fmt.Errorf("failed to find snapshotter %q", c.config.ContainerdConfig.Snapshotter)
	}

	c.imageFSPath = imageFSPath(config.ContainerdRootDir, config.ContainerdConfig.Snapshotter)
	log.L.Infof("Get image filesystem path %q", c.imageFSPath)

	if err := c.initPlatform(); err != nil {
		return nil, fmt.Errorf("initialize platform: %w", err)
	}

	// prepare streaming server
	c.streamServer, err = newStreamServer(c, config.StreamServerAddress, config.StreamServerPort, config.StreamIdleTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream server: %w", err)
	}

	c.eventMonitor = newEventMonitor(c)

	c.cniNetConfMonitor = make(map[string]*cniNetConfSyncer)
	for name, i := range c.netPlugin {
		path := c.config.NetworkPluginConfDir
		if name != defaultNetworkPlugin {
			if rc, ok := c.config.Runtimes[name]; ok {
				path = rc.NetworkPluginConfDir
			}
		}
		if path != "" {
			m, err := newCNINetConfSyncer(path, i, c.cniLoadOptions())
			if err != nil {
				return nil, fmt.Errorf("failed to create cni conf monitor for %s: %w", name, err)
			}
			c.cniNetConfMonitor[name] = m
		}
	}

	// Preload base OCI specs
	c.baseOCISpecs, err = loadBaseOCISpecs(&config)
	if err != nil {
		return nil, err
	}

	c.nri = nri

	return c, nil
}

// Register registers all required services onto a specific grpc server.
// This is used by containerd cri plugin.
func (c *criService) Register(s *grpc.Server) error {
	return c.register(s)
}

// RegisterTCP register all required services onto a GRPC server on TCP.
// This is used by containerd CRI plugin.
func (c *criService) RegisterTCP(s *grpc.Server) error {
	if !c.config.DisableTCPService {
		return c.register(s)
	}
	return nil
}

// receive and register the tenant id
func handleTenantRegistration(conn net.Conn) {
	defer conn.Close()

	// assume the tenant message is a TenantInfo, aka. {cid, port}
	var tenant_id string
	// max buf size: 256 bytes
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Printf("[Extended CRI shim] Failed to read from vsock: %v\n", err)
		return
	}
	fmt.Printf("[Extended CRI shim] Registering tenant [%s] ...\n", buf[:n])
	// get tenant id
	tenant_id = string(buf[:n])
	// get tenantinfo from conn
	remoteAddr := conn.RemoteAddr().String()
	remoteParts := strings.Split(remoteAddr, ":")
	// get cid from vm(cid)
	var num string
	parts := strings.Split(remoteParts[0], "(")
	if len(parts) > 1 {
		numParts := strings.Split(parts[1], ")")
		num = numParts[0]
	} else {
		fmt.Println("[Extended CRI shim] Cid not found")
	}
	remoteCID, _ := strconv.Atoi(num)
	remotePort, _ := strconv.Atoi(remoteParts[1])
	// save the tenant_id:tenantinfo into the tenant table
	fmt.Printf("[Extended CRI shim] Inserting [tenant_id: %s] into the TenantTable with [Cid %d Port %d] ...\n", tenant_id, remoteCID, remotePort)
	TenantTable[tenant_id] = TenantInfo{Cid: uint32(remoteCID), Port: uint32(remotePort)}
}

func StartVsockService(port uint32) {
	listener, err := vsock.Listen(port, nil)
	if err != nil {
		fmt.Printf("[Extended CRI shim] Failed to listen on vsock: %v\n", err)
		return
	}
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("[Extended CRI shim] Failed to accept vsock connection: %v\n", err)
			continue
		}
		go handleTenantRegistration(conn)
	}
}

// Run starts the CRI service.
func (c *criService) Run(ready func()) error {

	// listen to a specific vsock port (1234)
	fmt.Println("[Extended CRI shim] Start to listen on vsock")
	var vsock_port uint32 = 1234
	go StartVsockService(vsock_port)

	log.L.Info("Start subscribing containerd event")
	c.eventMonitor.subscribe(c.client)

	log.L.Infof("Start recovering state")
	if err := c.recover(ctrdutil.NamespacedContext()); err != nil {
		return fmt.Errorf("failed to recover state: %w", err)
	}

	// Start event handler.
	log.L.Info("Start event monitor")
	eventMonitorErrCh := c.eventMonitor.start()

	// Start snapshot stats syncer, it doesn't need to be stopped.
	log.L.Info("Start snapshots syncer")
	snapshotsSyncer := newSnapshotsSyncer(
		c.snapshotStore,
		c.client.SnapshotService(c.config.ContainerdConfig.Snapshotter),
		time.Duration(c.config.StatsCollectPeriod)*time.Second,
	)
	snapshotsSyncer.start()

	// Start CNI network conf syncers
	cniNetConfMonitorErrCh := make(chan error, len(c.cniNetConfMonitor))
	var netSyncGroup sync.WaitGroup
	for name, h := range c.cniNetConfMonitor {
		netSyncGroup.Add(1)
		log.L.Infof("Start cni network conf syncer for %s", name)
		go func(h *cniNetConfSyncer) {
			cniNetConfMonitorErrCh <- h.syncLoop()
			netSyncGroup.Done()
		}(h)
	}
	// For platforms that may not support CNI (darwin etc.) there's no
	// use in launching this as `Wait` will return immediately. Further
	// down we select on this channel along with some others to determine
	// if we should Close() the CRI service, so closing this preemptively
	// isn't good.
	if len(c.cniNetConfMonitor) > 0 {
		go func() {
			netSyncGroup.Wait()
			close(cniNetConfMonitorErrCh)
		}()
	}

	// Start streaming server.
	log.L.Info("Start streaming server")
	streamServerErrCh := make(chan error)
	go func() {
		defer close(streamServerErrCh)
		if err := c.streamServer.Start(true); err != nil && err != http.ErrServerClosed {
			log.L.WithError(err).Error("Failed to start streaming server")
			streamServerErrCh <- err
		}
	}()

	// register CRI domain with NRI
	if err := c.nri.Register(&criImplementation{c}); err != nil {
		return fmt.Errorf("failed to set up NRI for CRI service: %w", err)
	}

	// Set the server as initialized. GRPC services could start serving traffic.
	c.initialized.Store(true)
	ready()

	var eventMonitorErr, streamServerErr, cniNetConfMonitorErr error
	// Stop the whole CRI service if any of the critical service exits.
	select {
	case eventMonitorErr = <-eventMonitorErrCh:
	case streamServerErr = <-streamServerErrCh:
	case cniNetConfMonitorErr = <-cniNetConfMonitorErrCh:
	}
	if err := c.Close(); err != nil {
		return fmt.Errorf("failed to stop cri service: %w", err)
	}
	// If the error is set above, err from channel must be nil here, because
	// the channel is supposed to be closed. Or else, we wait and set it.
	if err := <-eventMonitorErrCh; err != nil {
		eventMonitorErr = err
	}
	log.L.Info("Event monitor stopped")
	if err := <-streamServerErrCh; err != nil {
		streamServerErr = err
	}
	log.L.Info("Stream server stopped")
	if eventMonitorErr != nil {
		return fmt.Errorf("event monitor error: %w", eventMonitorErr)
	}
	if streamServerErr != nil {
		return fmt.Errorf("stream server error: %w", streamServerErr)
	}
	if cniNetConfMonitorErr != nil {
		return fmt.Errorf("cni network conf monitor error: %w", cniNetConfMonitorErr)
	}

	return nil
}

// Close stops the CRI service.
// TODO(random-liu): Make close synchronous.
func (c *criService) Close() error {
	logrus.Info("Stop CRI service")
	for name, h := range c.cniNetConfMonitor {
		if err := h.stop(); err != nil {
			logrus.WithError(err).Errorf("failed to stop cni network conf monitor for %s", name)
		}
	}
	c.eventMonitor.stop()
	if err := c.streamServer.Stop(); err != nil {
		return fmt.Errorf("failed to stop stream server: %w", err)
	}
	return nil
}

// IsInitialized indicates whether CRI service has finished initialization.
func (c *criService) IsInitialized() bool {
	return c.initialized.Load()
}

func (c *criService) register(s *grpc.Server) error {
	instrumented := instrument.NewService(c)
	runtime.RegisterRuntimeServiceServer(s, instrumented)
	runtime.RegisterImageServiceServer(s, instrumented)
	return nil
}

// imageFSPath returns containerd image filesystem path.
// Note that if containerd changes directory layout, we also needs to change this.
func imageFSPath(rootDir, snapshotter string) string {
	return filepath.Join(rootDir, plugin.SnapshotPlugin.String()+"."+snapshotter)
}

func loadOCISpec(filename string) (*oci.Spec, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open base OCI spec: %s: %w", filename, err)
	}
	defer file.Close()

	spec := oci.Spec{}
	if err := json.NewDecoder(file).Decode(&spec); err != nil {
		return nil, fmt.Errorf("failed to parse base OCI spec file: %w", err)
	}

	return &spec, nil
}

func loadBaseOCISpecs(config *criconfig.Config) (map[string]*oci.Spec, error) {
	specs := map[string]*oci.Spec{}
	for _, cfg := range config.Runtimes {
		if cfg.BaseRuntimeSpec == "" {
			continue
		}

		// Don't load same file twice
		if _, ok := specs[cfg.BaseRuntimeSpec]; ok {
			continue
		}

		spec, err := loadOCISpec(cfg.BaseRuntimeSpec)
		if err != nil {
			return nil, fmt.Errorf("failed to load base OCI spec from file: %s: %w", cfg.BaseRuntimeSpec, err)
		}

		specs[cfg.BaseRuntimeSpec] = spec
	}

	return specs, nil
}
