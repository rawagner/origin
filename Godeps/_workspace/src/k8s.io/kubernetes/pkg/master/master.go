/*
Copyright 2014 The Kubernetes Authors All rights reserved.

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

package master

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/admission"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/latest"
	"k8s.io/kubernetes/pkg/api/meta"
	"k8s.io/kubernetes/pkg/api/rest"
	"k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/api/v1beta3"
	"k8s.io/kubernetes/pkg/apis/experimental"
	explatest "k8s.io/kubernetes/pkg/apis/experimental/latest"
	"k8s.io/kubernetes/pkg/apiserver"
	"k8s.io/kubernetes/pkg/auth/authenticator"
	"k8s.io/kubernetes/pkg/auth/authorizer"
	"k8s.io/kubernetes/pkg/auth/handlers"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/healthz"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/master/ports"
	"k8s.io/kubernetes/pkg/registry/componentstatus"
	controlleretcd "k8s.io/kubernetes/pkg/registry/controller/etcd"
	deploymentetcd "k8s.io/kubernetes/pkg/registry/deployment/etcd"
	"k8s.io/kubernetes/pkg/registry/endpoint"
	endpointsetcd "k8s.io/kubernetes/pkg/registry/endpoint/etcd"
	eventetcd "k8s.io/kubernetes/pkg/registry/event/etcd"
	expcontrolleretcd "k8s.io/kubernetes/pkg/registry/experimental/controller/etcd"
	jobetcd "k8s.io/kubernetes/pkg/registry/job/etcd"
	limitrangeetcd "k8s.io/kubernetes/pkg/registry/limitrange/etcd"
	"k8s.io/kubernetes/pkg/registry/namespace"
	namespaceetcd "k8s.io/kubernetes/pkg/registry/namespace/etcd"
	"k8s.io/kubernetes/pkg/registry/node"
	nodeetcd "k8s.io/kubernetes/pkg/registry/node/etcd"
	pvetcd "k8s.io/kubernetes/pkg/registry/persistentvolume/etcd"
	pvcetcd "k8s.io/kubernetes/pkg/registry/persistentvolumeclaim/etcd"
	podetcd "k8s.io/kubernetes/pkg/registry/pod/etcd"
	podtemplateetcd "k8s.io/kubernetes/pkg/registry/podtemplate/etcd"
	resourcequotaetcd "k8s.io/kubernetes/pkg/registry/resourcequota/etcd"
	secretetcd "k8s.io/kubernetes/pkg/registry/secret/etcd"
	sccetcd "k8s.io/kubernetes/pkg/registry/securitycontextconstraints/etcd"
	"k8s.io/kubernetes/pkg/registry/service"
	etcdallocator "k8s.io/kubernetes/pkg/registry/service/allocator/etcd"
	serviceetcd "k8s.io/kubernetes/pkg/registry/service/etcd"
	ipallocator "k8s.io/kubernetes/pkg/registry/service/ipallocator"
	serviceaccountetcd "k8s.io/kubernetes/pkg/registry/serviceaccount/etcd"
	thirdpartyresourceetcd "k8s.io/kubernetes/pkg/registry/thirdpartyresource/etcd"
	"k8s.io/kubernetes/pkg/registry/thirdpartyresourcedata"
	thirdpartyresourcedataetcd "k8s.io/kubernetes/pkg/registry/thirdpartyresourcedata/etcd"
	"k8s.io/kubernetes/pkg/storage"
	etcdstorage "k8s.io/kubernetes/pkg/storage/etcd"
	"k8s.io/kubernetes/pkg/tools"
	//"k8s.io/kubernetes/pkg/ui"
	"k8s.io/kubernetes/pkg/util"
	"k8s.io/kubernetes/pkg/util/sets"

	daemonetcd "k8s.io/kubernetes/pkg/registry/daemonset/etcd"
	horizontalpodautoscaleretcd "k8s.io/kubernetes/pkg/registry/horizontalpodautoscaler/etcd"

	"github.com/emicklei/go-restful"
	"github.com/emicklei/go-restful/swagger"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/kubernetes/pkg/registry/service/allocator"
	"k8s.io/kubernetes/pkg/registry/service/portallocator"
)

const (
	DefaultEtcdPathPrefix = "/registry"
)

// Config is a structure used to configure a Master.
type Config struct {
	DatabaseStorage    storage.Interface
	ExpDatabaseStorage storage.Interface
	EventTTL           time.Duration
	NodeRegexp         string
	KubeletClient      client.KubeletClient
	// allow downstream consumers to disable the core controller loops
	EnableCoreControllers bool
	EnableLogsSupport     bool
	EnableUISupport       bool
	// allow downstream consumers to disable swagger
	EnableSwaggerSupport bool
	// allow v1beta3 to be conditionally enabled
	EnableV1Beta3 bool
	// allow api versions to be conditionally disabled
	DisableV1 bool
	EnableExp bool
	// allow downstream consumers to disable the index route
	EnableIndex           bool
	EnableProfiling       bool
	EnableWatchCache      bool
	APIPrefix             string
	ExpAPIPrefix          string
	CorsAllowedOriginList []string
	Authenticator         authenticator.Request
	// TODO(roberthbailey): Remove once the server no longer supports http basic auth.
	SupportsBasicAuth      bool
	Authorizer             authorizer.Authorizer
	AdmissionControl       admission.Interface
	MasterServiceNamespace string

	// Map requests to contexts. Exported so downstream consumers can provider their own mappers
	RequestContextMapper api.RequestContextMapper

	// If specified, all web services will be registered into this container
	RestfulContainer *restful.Container

	// If specified, requests will be allocated a random timeout between this value, and twice this value.
	// Note that it is up to the request handlers to ignore or honor this timeout. In seconds.
	MinRequestTimeout int

	// Number of masters running; all masters must be started with the
	// same value for this field. (Numbers > 1 currently untested.)
	MasterCount int

	// The port on PublicAddress where a read-write server will be installed.
	// Defaults to 6443 if not set.
	ReadWritePort int

	// ExternalHost is the host name to use for external (public internet) facing URLs (e.g. Swagger)
	ExternalHost string

	// PublicAddress is the IP address where members of the cluster (kubelet,
	// kube-proxy, services, etc.) can reach the master.
	// If nil or 0.0.0.0, the host's default interface will be used.
	PublicAddress net.IP

	// Control the interval that pod, node IP, and node heath status caches
	// expire.
	CacheTimeout time.Duration

	// The name of the cluster.
	ClusterName string

	// The range of IPs to be assigned to services with type=ClusterIP or greater
	ServiceClusterIPRange *net.IPNet

	// The IP address for the master service (must be inside ServiceClusterIPRange
	ServiceReadWriteIP net.IP

	// The range of ports to be assigned to services with type=NodePort or greater
	ServiceNodePortRange util.PortRange

	// Used to customize default proxy dial/tls options
	ProxyDialer          apiserver.ProxyDialerFunc
	ProxyTLSClientConfig *tls.Config

	// Used to start and monitor tunneling
	Tunneler Tunneler
}

type InstallSSHKey func(user string, data []byte) error

// Master contains state for a Kubernetes cluster master/api server.
type Master struct {
	// "Inputs", Copied from Config
	serviceClusterIPRange *net.IPNet
	serviceNodePortRange  util.PortRange
	cacheTimeout          time.Duration
	minRequestTimeout     time.Duration

	mux                   apiserver.Mux
	muxHelper             *apiserver.MuxHelper
	handlerContainer      *restful.Container
	rootWebService        *restful.WebService
	enableCoreControllers bool
	enableLogsSupport     bool
	enableUISupport       bool
	enableSwaggerSupport  bool
	enableProfiling       bool
	enableWatchCache      bool
	apiPrefix             string
	expAPIPrefix          string
	corsAllowedOriginList []string
	authenticator         authenticator.Request
	authorizer            authorizer.Authorizer
	admissionControl      admission.Interface
	masterCount           int
	v1beta3               bool
	v1                    bool
	exp                   bool
	requestContextMapper  api.RequestContextMapper

	// External host is the name that should be used in external (public internet) URLs for this master
	externalHost string
	// clusterIP is the IP address of the master within the cluster.
	clusterIP            net.IP
	publicReadWritePort  int
	serviceReadWriteIP   net.IP
	serviceReadWritePort int
	masterServices       *util.Runner

	// storage contains the RESTful endpoints exposed by this master
	storage map[string]rest.Storage

	// registries are internal client APIs for accessing the storage layer
	// TODO: define the internal typed interface in a way that clients can
	// also be replaced
	nodeRegistry              node.Registry
	namespaceRegistry         namespace.Registry
	serviceRegistry           service.Registry
	endpointRegistry          endpoint.Registry
	serviceClusterIPAllocator service.RangeRegistry
	serviceNodePortAllocator  service.RangeRegistry

	// "Outputs"
	Handler         http.Handler
	InsecureHandler http.Handler

	// Used for custom proxy dialing, and proxy TLS options
	proxyTransport http.RoundTripper

	// Used to start and monitor tunneling
	tunneler Tunneler

	// storage for third party objects
	thirdPartyStorage storage.Interface
}

// NewEtcdStorage returns a storage.Interface for the provided arguments or an error if the version
// is incorrect.
func NewEtcdStorage(client tools.EtcdClient, interfacesFunc meta.VersionInterfacesFunc, version, prefix string) (etcdStorage storage.Interface, err error) {
	versionInterfaces, err := interfacesFunc(version)
	if err != nil {
		return etcdStorage, err
	}
	return etcdstorage.NewEtcdStorage(client, versionInterfaces.Codec, prefix), nil
}

// setDefaults fills in any fields not set that are required to have valid data.
func setDefaults(c *Config) {
	if c.ServiceClusterIPRange == nil {
		defaultNet := "10.0.0.0/24"
		glog.Warningf("Network range for service cluster IPs is unspecified. Defaulting to %v.", defaultNet)
		_, serviceClusterIPRange, err := net.ParseCIDR(defaultNet)
		if err != nil {
			glog.Fatalf("Unable to parse CIDR: %v", err)
		}
		if size := ipallocator.RangeSize(serviceClusterIPRange); size < 8 {
			glog.Fatalf("The service cluster IP range must be at least %d IP addresses", 8)
		}
		c.ServiceClusterIPRange = serviceClusterIPRange
	}
	if c.ServiceReadWriteIP == nil {
		// Select the first valid IP from ServiceClusterIPRange to use as the master service IP.
		serviceReadWriteIP, err := ipallocator.GetIndexedIP(c.ServiceClusterIPRange, 1)
		if err != nil {
			glog.Fatalf("Failed to generate service read-write IP for master service: %v", err)
		}
		glog.V(4).Infof("Setting master service IP to %q (read-write).", serviceReadWriteIP)
		c.ServiceReadWriteIP = serviceReadWriteIP
	}
	if c.ServiceNodePortRange.Size == 0 {
		// TODO: Currently no way to specify an empty range (do we need to allow this?)
		// We should probably allow this for clouds that don't require NodePort to do load-balancing (GCE)
		// but then that breaks the strict nestedness of ServiceType.
		// Review post-v1
		defaultServiceNodePortRange := util.PortRange{Base: 30000, Size: 2768}
		c.ServiceNodePortRange = defaultServiceNodePortRange
		glog.Infof("Node port range unspecified. Defaulting to %v.", c.ServiceNodePortRange)
	}
	if c.MasterCount == 0 {
		// Clearly, there will be at least one master.
		c.MasterCount = 1
	}
	if c.ReadWritePort == 0 {
		c.ReadWritePort = 6443
	}
	if c.CacheTimeout == 0 {
		c.CacheTimeout = 5 * time.Second
	}
	for c.PublicAddress == nil || c.PublicAddress.IsUnspecified() || c.PublicAddress.IsLoopback() {
		// TODO: This should be done in the caller and just require a
		// valid value to be passed in.
		hostIP, err := util.ChooseHostInterface()
		if err != nil {
			glog.Fatalf("Unable to find suitable network address.error='%v' . "+
				"Will try again in 5 seconds. Set the public address directly to avoid this wait.", err)
			time.Sleep(5 * time.Second)
		}
		c.PublicAddress = hostIP
		glog.Infof("Will report %v as public IP address.", c.PublicAddress)
	}
	if c.RequestContextMapper == nil {
		c.RequestContextMapper = api.NewRequestContextMapper()
	}
}

// New returns a new instance of Master from the given config.
// Certain config fields will be set to a default value if unset,
// including:
//   ServiceClusterIPRange
//   ServiceNodePortRange
//   MasterCount
//   ReadWritePort
//   PublicAddress
// Certain config fields must be specified, including:
//   KubeletClient
// Public fields:
//   Handler -- The returned master has a field TopHandler which is an
//   http.Handler which handles all the endpoints provided by the master,
//   including the API, the UI, and miscellaneous debugging endpoints.  All
//   these are subject to authorization and authentication.
//   InsecureHandler -- an http.Handler which handles all the same
//   endpoints as Handler, but no authorization and authentication is done.
// Public methods:
//   HandleWithAuth -- Allows caller to add an http.Handler for an endpoint
//   that uses the same authentication and authorization (if any is configured)
//   as the master's built-in endpoints.
//   If the caller wants to add additional endpoints not using the master's
//   auth, then the caller should create a handler for those endpoints, which delegates the
//   any unhandled paths to "Handler".
func New(c *Config) *Master {
	setDefaults(c)
	if c.KubeletClient == nil {
		glog.Fatalf("master.New() called with config.KubeletClient == nil")
	}

	m := &Master{
		serviceClusterIPRange: c.ServiceClusterIPRange,
		serviceNodePortRange:  c.ServiceNodePortRange,
		rootWebService:        new(restful.WebService),
		enableCoreControllers: c.EnableCoreControllers,
		enableLogsSupport:     c.EnableLogsSupport,
		enableUISupport:       c.EnableUISupport,
		enableSwaggerSupport:  c.EnableSwaggerSupport,
		enableProfiling:       c.EnableProfiling,
		enableWatchCache:      c.EnableWatchCache,
		apiPrefix:             c.APIPrefix,
		expAPIPrefix:          c.ExpAPIPrefix,
		corsAllowedOriginList: c.CorsAllowedOriginList,
		authenticator:         c.Authenticator,
		authorizer:            c.Authorizer,
		admissionControl:      c.AdmissionControl,
		v1beta3:               c.EnableV1Beta3,
		v1:                    !c.DisableV1,
		exp:                   c.EnableExp,
		requestContextMapper:  c.RequestContextMapper,

		cacheTimeout:      c.CacheTimeout,
		minRequestTimeout: time.Duration(c.MinRequestTimeout) * time.Second,

		masterCount:         c.MasterCount,
		externalHost:        c.ExternalHost,
		clusterIP:           c.PublicAddress,
		publicReadWritePort: c.ReadWritePort,
		serviceReadWriteIP:  c.ServiceReadWriteIP,
		// TODO: serviceReadWritePort should be passed in as an argument, it may not always be 443
		serviceReadWritePort: 443,

		tunneler: c.Tunneler,
	}

	var handlerContainer *restful.Container
	if c.RestfulContainer != nil {
		m.mux = c.RestfulContainer.ServeMux
		handlerContainer = c.RestfulContainer
	} else {
		mux := http.NewServeMux()
		m.mux = mux
		handlerContainer = NewHandlerContainer(mux)
	}
	m.handlerContainer = handlerContainer
	// Use CurlyRouter to be able to use regular expressions in paths. Regular expressions are required in paths for example for proxy (where the path is proxy/{kind}/{name}/{*})
	m.handlerContainer.Router(restful.CurlyRouter{})
	m.muxHelper = &apiserver.MuxHelper{Mux: m.mux, RegisteredPaths: []string{}}

	m.init(c)

	return m
}

// HandleWithAuth adds an http.Handler for pattern to an http.ServeMux
// Applies the same authentication and authorization (if any is configured)
// to the request is used for the master's built-in endpoints.
func (m *Master) HandleWithAuth(pattern string, handler http.Handler) {
	// TODO: Add a way for plugged-in endpoints to translate their
	// URLs into attributes that an Authorizer can understand, and have
	// sensible policy defaults for plugged-in endpoints.  This will be different
	// for generic endpoints versus REST object endpoints.
	// TODO: convert to go-restful
	m.muxHelper.Handle(pattern, handler)
}

// HandleFuncWithAuth adds an http.Handler for pattern to an http.ServeMux
// Applies the same authentication and authorization (if any is configured)
// to the request is used for the master's built-in endpoints.
func (m *Master) HandleFuncWithAuth(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	// TODO: convert to go-restful
	m.muxHelper.HandleFunc(pattern, handler)
}

func NewHandlerContainer(mux *http.ServeMux) *restful.Container {
	container := restful.NewContainer()
	container.ServeMux = mux
	apiserver.InstallRecoverHandler(container)
	return container
}

// init initializes master.
func (m *Master) init(c *Config) {

	if c.ProxyDialer != nil || c.ProxyTLSClientConfig != nil {
		m.proxyTransport = util.SetTransportDefaults(&http.Transport{
			Dial:            c.ProxyDialer,
			TLSClientConfig: c.ProxyTLSClientConfig,
		})
	}

	healthzChecks := []healthz.HealthzChecker{}
	podStorage := podetcd.NewStorage(c.DatabaseStorage, c.EnableWatchCache, c.KubeletClient, m.proxyTransport)

	podTemplateStorage := podtemplateetcd.NewREST(c.DatabaseStorage)

	eventStorage := eventetcd.NewREST(c.DatabaseStorage, uint64(c.EventTTL.Seconds()))
	limitRangeStorage := limitrangeetcd.NewREST(c.DatabaseStorage)

	resourceQuotaStorage, resourceQuotaStatusStorage := resourcequotaetcd.NewREST(c.DatabaseStorage)
	secretStorage := secretetcd.NewREST(c.DatabaseStorage)
	serviceAccountStorage := serviceaccountetcd.NewREST(c.DatabaseStorage)
	persistentVolumeStorage, persistentVolumeStatusStorage := pvetcd.NewREST(c.DatabaseStorage)
	persistentVolumeClaimStorage, persistentVolumeClaimStatusStorage := pvcetcd.NewREST(c.DatabaseStorage)

	namespaceStorage, namespaceStatusStorage, namespaceFinalizeStorage := namespaceetcd.NewREST(c.DatabaseStorage)
	m.namespaceRegistry = namespace.NewRegistry(namespaceStorage)

	endpointsStorage := endpointsetcd.NewREST(c.DatabaseStorage, c.EnableWatchCache)
	m.endpointRegistry = endpoint.NewRegistry(endpointsStorage)

	securityContextConstraintsStorage := sccetcd.NewStorage(c.DatabaseStorage)

	nodeStorage, nodeStatusStorage := nodeetcd.NewREST(c.DatabaseStorage, c.EnableWatchCache, c.KubeletClient, m.proxyTransport)
	m.nodeRegistry = node.NewRegistry(nodeStorage)

	serviceStorage := serviceetcd.NewREST(c.DatabaseStorage)
	m.serviceRegistry = service.NewRegistry(serviceStorage)

	var serviceClusterIPRegistry service.RangeRegistry
	serviceClusterIPAllocator := ipallocator.NewAllocatorCIDRRange(m.serviceClusterIPRange, func(max int, rangeSpec string) allocator.Interface {
		mem := allocator.NewAllocationMap(max, rangeSpec)
		etcd := etcdallocator.NewEtcd(mem, "/ranges/serviceips", "serviceipallocation", c.DatabaseStorage)
		serviceClusterIPRegistry = etcd
		return etcd
	})
	m.serviceClusterIPAllocator = serviceClusterIPRegistry

	var serviceNodePortRegistry service.RangeRegistry
	serviceNodePortAllocator := portallocator.NewPortAllocatorCustom(m.serviceNodePortRange, func(max int, rangeSpec string) allocator.Interface {
		mem := allocator.NewAllocationMap(max, rangeSpec)
		etcd := etcdallocator.NewEtcd(mem, "/ranges/servicenodeports", "servicenodeportallocation", c.DatabaseStorage)
		serviceNodePortRegistry = etcd
		return etcd
	})
	m.serviceNodePortAllocator = serviceNodePortRegistry

	controllerStorage := controlleretcd.NewREST(c.DatabaseStorage)

	// TODO: Factor out the core API registration
	m.storage = map[string]rest.Storage{
		"pods":             podStorage.Pod,
		"pods/attach":      podStorage.Attach,
		"pods/status":      podStorage.Status,
		"pods/log":         podStorage.Log,
		"pods/exec":        podStorage.Exec,
		"pods/portforward": podStorage.PortForward,
		"pods/proxy":       podStorage.Proxy,
		"pods/binding":     podStorage.Binding,
		"bindings":         podStorage.Binding,

		"podTemplates": podTemplateStorage,

		"replicationControllers": controllerStorage,
		"services":               service.NewStorage(m.serviceRegistry, m.endpointRegistry, serviceClusterIPAllocator, serviceNodePortAllocator, m.proxyTransport),
		"endpoints":              endpointsStorage,
		"nodes":                  nodeStorage,
		"nodes/status":           nodeStatusStorage,
		"events":                 eventStorage,

		"limitRanges":                   limitRangeStorage,
		"resourceQuotas":                resourceQuotaStorage,
		"resourceQuotas/status":         resourceQuotaStatusStorage,
		"namespaces":                    namespaceStorage,
		"namespaces/status":             namespaceStatusStorage,
		"namespaces/finalize":           namespaceFinalizeStorage,
		"secrets":                       secretStorage,
		"serviceAccounts":               serviceAccountStorage,
		"securityContextConstraints":    securityContextConstraintsStorage,
		"persistentVolumes":             persistentVolumeStorage,
		"persistentVolumes/status":      persistentVolumeStatusStorage,
		"persistentVolumeClaims":        persistentVolumeClaimStorage,
		"persistentVolumeClaims/status": persistentVolumeClaimStatusStorage,

		"componentStatuses": componentstatus.NewStorage(func() map[string]apiserver.Server { return m.getServersToValidate(c) }),
	}

	if m.tunneler != nil {
		m.tunneler.Run(m.getNodeAddresses)
		healthzChecks = append(healthzChecks, healthz.NamedCheck("SSH Tunnel Check", m.IsTunnelSyncHealthy))
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "apiserver_proxy_tunnel_sync_latency_secs",
			Help: "The time since the last successful synchronization of the SSH tunnels for proxy requests.",
		}, func() float64 { return float64(m.tunneler.SecondsSinceSync()) })
	}

	apiVersions := []string{}
	if m.v1beta3 {
		if err := m.api_v1beta3().InstallREST(m.handlerContainer); err != nil {
			glog.Fatalf("Unable to setup API v1beta3: %v", err)
		}
		apiVersions = append(apiVersions, "v1beta3")
	}
	if m.v1 {
		if err := m.api_v1().InstallREST(m.handlerContainer); err != nil {
			glog.Fatalf("Unable to setup API v1: %v", err)
		}
		apiVersions = append(apiVersions, "v1")
	}

	apiserver.InstallSupport(m.muxHelper, m.rootWebService, c.EnableProfiling, healthzChecks...)
	apiserver.AddApiWebService(m.handlerContainer, c.APIPrefix, apiVersions)
	defaultVersion := m.defaultAPIGroupVersion()
	requestInfoResolver := &apiserver.APIRequestInfoResolver{APIPrefixes: sets.NewString(strings.TrimPrefix(defaultVersion.Root, "/")), RestMapper: defaultVersion.Mapper}
	apiserver.InstallServiceErrorHandler(m.handlerContainer, requestInfoResolver, apiVersions)

	if m.exp {
		expVersion := m.experimental(c)
		if err := expVersion.InstallREST(m.handlerContainer); err != nil {
			glog.Fatalf("Unable to setup experimental api: %v", err)
		}
		apiserver.AddApiWebService(m.handlerContainer, c.ExpAPIPrefix, []string{expVersion.Version})
		expRequestInfoResolver := &apiserver.APIRequestInfoResolver{APIPrefixes: sets.NewString(strings.TrimPrefix(expVersion.Root, "/")), RestMapper: expVersion.Mapper}
		apiserver.InstallServiceErrorHandler(m.handlerContainer, expRequestInfoResolver, []string{expVersion.Version})
	}

	// Register root handler.
	// We do not register this using restful Webservice since we do not want to surface this in api docs.
	// Allow master to be embedded in contexts which already have something registered at the root
	if c.EnableIndex {
		m.mux.HandleFunc("/", apiserver.IndexHandler(m.handlerContainer, m.muxHelper))
	}

	if c.EnableLogsSupport {
		apiserver.InstallLogsSupport(m.muxHelper)
	}
	/*if c.EnableUISupport {
		ui.InstallSupport(m.mux)
	}*/

	if c.EnableProfiling {
		m.mux.HandleFunc("/debug/pprof/", pprof.Index)
		m.mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		m.mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	}

	handler := http.Handler(m.mux.(*http.ServeMux))

	// TODO: handle CORS and auth using go-restful
	// See github.com/emicklei/go-restful/blob/master/examples/restful-CORS-filter.go, and
	// github.com/emicklei/go-restful/blob/master/examples/restful-basic-authentication.go

	if len(c.CorsAllowedOriginList) > 0 {
		allowedOriginRegexps, err := util.CompileRegexps(c.CorsAllowedOriginList)
		if err != nil {
			glog.Fatalf("Invalid CORS allowed origin, --cors-allowed-origins flag was set to %v - %v", strings.Join(c.CorsAllowedOriginList, ","), err)
		}
		handler = apiserver.CORS(handler, allowedOriginRegexps, nil, nil, "true")
	}

	m.InsecureHandler = handler

	attributeGetter := apiserver.NewRequestAttributeGetter(m.requestContextMapper, latest.RESTMapper, "api")
	handler = apiserver.WithAuthorizationCheck(handler, attributeGetter, m.authorizer)

	// Install Authenticator
	if c.Authenticator != nil {
		authenticatedHandler, err := handlers.NewRequestAuthenticator(m.requestContextMapper, c.Authenticator, handlers.Unauthorized(c.SupportsBasicAuth), handler)
		if err != nil {
			glog.Fatalf("Could not initialize authenticator: %v", err)
		}
		handler = authenticatedHandler
	}

	// Install root web services
	m.handlerContainer.Add(m.rootWebService)

	// TODO: Make this optional?  Consumers of master depend on this currently.
	m.Handler = handler

	if m.enableSwaggerSupport {
		m.InstallSwaggerAPI()
	}

	// After all wrapping is done, put a context filter around both handlers
	if handler, err := api.NewRequestContextFilter(m.requestContextMapper, m.Handler); err != nil {
		glog.Fatalf("Could not initialize request context filter: %v", err)
	} else {
		m.Handler = handler
	}

	if handler, err := api.NewRequestContextFilter(m.requestContextMapper, m.InsecureHandler); err != nil {
		glog.Fatalf("Could not initialize request context filter: %v", err)
	} else {
		m.InsecureHandler = handler
	}

	// TODO: Attempt clean shutdown?
	if m.enableCoreControllers {
		m.NewBootstrapController().Start()
	}
}

// NewBootstrapController returns a controller for watching the core capabilities of the master.
func (m *Master) NewBootstrapController() *Controller {
	return &Controller{
		NamespaceRegistry: m.namespaceRegistry,
		ServiceRegistry:   m.serviceRegistry,
		MasterCount:       m.masterCount,

		EndpointRegistry: m.endpointRegistry,
		EndpointInterval: 10 * time.Second,

		ServiceClusterIPRegistry: m.serviceClusterIPAllocator,
		ServiceClusterIPRange:    m.serviceClusterIPRange,
		ServiceClusterIPInterval: 3 * time.Minute,

		ServiceNodePortRegistry: m.serviceNodePortAllocator,
		ServiceNodePortRange:    m.serviceNodePortRange,
		ServiceNodePortInterval: 3 * time.Minute,

		PublicIP: m.clusterIP,

		ServiceIP:         m.serviceReadWriteIP,
		ServicePort:       m.serviceReadWritePort,
		PublicServicePort: m.publicReadWritePort,
	}
}

// InstallSwaggerAPI installs the /swaggerapi/ endpoint to allow schema discovery
// and traversal.  It is optional to allow consumers of the Kubernetes master to
// register their own web services into the Kubernetes mux prior to initialization
// of swagger, so that other resource types show up in the documentation.
func (m *Master) InstallSwaggerAPI() {
	hostAndPort := m.externalHost
	protocol := "https://"

	// TODO: this is kind of messed up, we should just pipe in the full URL from the outside, rather
	// than guessing at it.
	if len(m.externalHost) == 0 && m.clusterIP != nil {
		host := m.clusterIP.String()
		if m.publicReadWritePort != 0 {
			hostAndPort = net.JoinHostPort(host, strconv.Itoa(m.publicReadWritePort))
		}
	}
	webServicesUrl := protocol + hostAndPort

	// Enable swagger UI and discovery API
	swaggerConfig := swagger.Config{
		WebServicesUrl:  webServicesUrl,
		WebServices:     m.handlerContainer.RegisteredWebServices(),
		ApiPath:         "/swaggerapi/",
		SwaggerPath:     "/swaggerui/",
		SwaggerFilePath: "/swagger-ui/",
	}
	swagger.RegisterSwaggerService(swaggerConfig, m.handlerContainer)
}

func (m *Master) getServersToValidate(c *Config) map[string]apiserver.Server {
	serversToValidate := map[string]apiserver.Server{
		"controller-manager": {Addr: "127.0.0.1", Port: ports.ControllerManagerPort, Path: "/healthz"},
		"scheduler":          {Addr: "127.0.0.1", Port: ports.SchedulerPort, Path: "/healthz"},
	}
	for ix, machine := range c.DatabaseStorage.Backends() {
		etcdUrl, err := url.Parse(machine)
		if err != nil {
			glog.Errorf("Failed to parse etcd url for validation: %v", err)
			continue
		}
		var port int
		var addr string
		if strings.Contains(etcdUrl.Host, ":") {
			var portString string
			addr, portString, err = net.SplitHostPort(etcdUrl.Host)
			if err != nil {
				glog.Errorf("Failed to split host/port: %s (%v)", etcdUrl.Host, err)
				continue
			}
			port, _ = strconv.Atoi(portString)
		} else {
			addr = etcdUrl.Host
			port = 4001
		}
		serversToValidate[fmt.Sprintf("etcd-%d", ix)] = apiserver.Server{Addr: addr, Port: port, Path: "/health", Validate: etcdstorage.EtcdHealthCheck}
	}
	return serversToValidate
}

func (m *Master) defaultAPIGroupVersion() *apiserver.APIGroupVersion {
	return &apiserver.APIGroupVersion{
		Root: m.apiPrefix,

		Mapper: latest.RESTMapper,

		Creater:   api.Scheme,
		Convertor: api.Scheme,
		Typer:     api.Scheme,
		Linker:    latest.SelfLinker,

		Admit:   m.admissionControl,
		Context: m.requestContextMapper,

		MinRequestTimeout: m.minRequestTimeout,
	}
}

// api_v1beta3 returns the resources and codec for API version v1beta3.
func (m *Master) api_v1beta3() *apiserver.APIGroupVersion {
	storage := make(map[string]rest.Storage)
	for k, v := range m.storage {
		if k == "minions" || k == "minions/status" {
			continue
		}
		storage[strings.ToLower(k)] = v
	}
	version := m.defaultAPIGroupVersion()
	version.Storage = storage
	version.Version = "v1beta3"
	version.Codec = v1beta3.Codec
	return version
}

// api_v1 returns the resources and codec for API version v1.
func (m *Master) api_v1() *apiserver.APIGroupVersion {
	storage := make(map[string]rest.Storage)
	for k, v := range m.storage {
		storage[strings.ToLower(k)] = v
	}
	version := m.defaultAPIGroupVersion()
	version.Storage = storage
	version.Version = "v1"
	version.Codec = v1.Codec
	return version
}

func (m *Master) InstallThirdPartyAPI(rsrc *experimental.ThirdPartyResource) error {
	kind, group, err := thirdpartyresourcedata.ExtractApiGroupAndKind(rsrc)
	if err != nil {
		return err
	}
	thirdparty := m.thirdpartyapi(group, kind, rsrc.Versions[0].Name)
	if err := thirdparty.InstallREST(m.handlerContainer); err != nil {
		glog.Fatalf("Unable to setup thirdparty api: %v", err)
	}
	thirdPartyPrefix := "/thirdparty/" + group + "/"
	apiserver.AddApiWebService(m.handlerContainer, thirdPartyPrefix, []string{rsrc.Versions[0].Name})
	thirdPartyRequestInfoResolver := &apiserver.APIRequestInfoResolver{APIPrefixes: sets.NewString(strings.TrimPrefix(group, "/")), RestMapper: thirdparty.Mapper}
	apiserver.InstallServiceErrorHandler(m.handlerContainer, thirdPartyRequestInfoResolver, []string{thirdparty.Version})
	return nil
}

func (m *Master) thirdpartyapi(group, kind, version string) *apiserver.APIGroupVersion {
	resourceStorage := thirdpartyresourcedataetcd.NewREST(m.thirdPartyStorage, group, kind)

	apiRoot := "/thirdparty/" + group + "/"

	storage := map[string]rest.Storage{
		strings.ToLower(kind) + "s": resourceStorage,
	}

	return &apiserver.APIGroupVersion{
		Root: apiRoot,

		Creater:   thirdpartyresourcedata.NewObjectCreator(version, api.Scheme),
		Convertor: api.Scheme,
		Typer:     api.Scheme,

		Mapper:  thirdpartyresourcedata.NewMapper(explatest.RESTMapper, kind, version),
		Codec:   explatest.Codec,
		Linker:  explatest.SelfLinker,
		Storage: storage,
		Version: version,

		Admit:   m.admissionControl,
		Context: m.requestContextMapper,

		MinRequestTimeout: m.minRequestTimeout,
	}
}

// experimental returns the resources and codec for the experimental api
func (m *Master) experimental(c *Config) *apiserver.APIGroupVersion {
	controllerStorage := expcontrolleretcd.NewStorage(c.ExpDatabaseStorage)
	autoscalerStorage := horizontalpodautoscaleretcd.NewREST(c.ExpDatabaseStorage)
	thirdPartyResourceStorage := thirdpartyresourceetcd.NewREST(c.ExpDatabaseStorage)
	daemonSetStorage := daemonetcd.NewREST(c.ExpDatabaseStorage)
	deploymentStorage := deploymentetcd.NewREST(c.ExpDatabaseStorage)
	jobStorage := jobetcd.NewREST(c.ExpDatabaseStorage)

	storage := map[string]rest.Storage{
		strings.ToLower("replicationControllers"):       controllerStorage.ReplicationController,
		strings.ToLower("replicationControllers/scale"): controllerStorage.Scale,
		strings.ToLower("horizontalpodautoscalers"):     autoscalerStorage,
		strings.ToLower("thirdpartyresources"):          thirdPartyResourceStorage,
		strings.ToLower("daemonsets"):                   daemonSetStorage,
		strings.ToLower("deployments"):                  deploymentStorage,
		strings.ToLower("jobs"):                         jobStorage,
	}

	return &apiserver.APIGroupVersion{
		Root: m.expAPIPrefix,

		Creater:   api.Scheme,
		Convertor: api.Scheme,
		Typer:     api.Scheme,

		Mapper:  explatest.RESTMapper,
		Codec:   explatest.Codec,
		Linker:  explatest.SelfLinker,
		Storage: storage,
		Version: explatest.Version,

		Admit:   m.admissionControl,
		Context: m.requestContextMapper,

		MinRequestTimeout: m.minRequestTimeout,
	}
}

// findExternalAddress returns ExternalIP of provided node with fallback to LegacyHostIP.
func findExternalAddress(node *api.Node) (string, error) {
	var fallback string
	for ix := range node.Status.Addresses {
		addr := &node.Status.Addresses[ix]
		if addr.Type == api.NodeExternalIP {
			return addr.Address, nil
		}
		if fallback == "" && addr.Type == api.NodeLegacyHostIP {
			fallback = addr.Address
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("Couldn't find external address: %v", node)
}

func (m *Master) getNodeAddresses() ([]string, error) {
	nodes, err := m.nodeRegistry.ListNodes(api.NewDefaultContext(), labels.Everything(), fields.Everything())
	if err != nil {
		return nil, err
	}
	addrs := []string{}
	for ix := range nodes.Items {
		node := &nodes.Items[ix]
		addr, err := findExternalAddress(node)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func (m *Master) IsTunnelSyncHealthy(req *http.Request) error {
	if m.tunneler == nil {
		return nil
	}
	lag := m.tunneler.SecondsSinceSync()
	if lag > 600 {
		return fmt.Errorf("Tunnel sync is taking to long: %d", lag)
	}
	return nil
}
