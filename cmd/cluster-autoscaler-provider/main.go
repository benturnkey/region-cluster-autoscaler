package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	upstreamaws "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/aws"
	upstreamwrapper "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/examples/external-grpc-cloud-provider-service/wrapper"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/protos"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	coreoptions "k8s.io/autoscaler/cluster-autoscaler/core/options"
	kube_flag "k8s.io/component-base/cli/flag"
	klog "k8s.io/klog/v2"
)

type multiStringFlag []string

func (f *multiStringFlag) String() string {
	return "[" + strings.Join(*f, " ") + "]"
}

func (f *multiStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func registerMultiStringFlag(name string, usage string) *multiStringFlag {
	value := new(multiStringFlag)
	flag.Var(value, name, usage)
	return value
}

var (
	mode       = flag.String("mode", "provider", "Execution mode: 'router' or 'provider'.")
	configPath = flag.String("config", "", "Path to the shared configuration file.")
	region     = flag.String("region", "", "The AWS region this instance is responsible for (only used in 'provider' mode).")

	routerAddress = flag.String("router-address", ":8086", "The address to expose the router gRPC service.")
	httpAddress   = flag.String("http-address", ":8080", "The address to expose the router HTTP health and metrics service.")
	keyCert       = flag.String("key-cert", "", "The path to the certificate key file. Empty string for insecure communication.")
	cert          = flag.String("cert", "", "The path to the certificate file. Empty string for insecure communication.")
	cacert        = flag.String("ca-cert", "", "The path to the ca certificate file. Empty string for insecure communication.")

	cloudConfig = flag.String("cloud-config", "", "The path to the cloud provider configuration file. Empty string for no configuration file.")
	clusterName = flag.String("cluster-name", "", "Autoscaled cluster name, if available.")

	nodeGroupsFlag = registerMultiStringFlag(
		"nodes",
		"Sets min,max size and other configuration data for a node group in a format accepted by the cloud provider. Can be used multiple times.")
	nodeGroupAutoDiscoveryFlag = registerMultiStringFlag(
		"node-group-auto-discovery",
		"One or more definition(s) of node group auto-discovery. AWS matches by ASG tags, for example `asg:tag=tagKey,anotherTagKey`.")

	awsUseStaticInstanceList = flag.Bool("aws-use-static-instance-list", false, "Use the generated static EC2 instance type list instead of calling AWS APIs at startup.")
	dryRun                   = flag.Bool("dry-run", false, "Enable dry-run mode: initialize clients and serve gRPC, but log mutating actions instead of performing them.")
)

func main() {
	klog.InitFlags(nil)
	kube_flag.InitFlags()
	flag.Parse()

	if *configPath == "" {
		klog.Fatal("--config is required")
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		klog.Fatalf("failed to load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *mode {
	case "router":
		runRouter(ctx, cfg)
	case "provider":
		runProvider(ctx, cfg)
	default:
		klog.Fatalf("invalid mode %q", *mode)
	}
}

func runProvider(ctx context.Context, cfg *Config) {
	if *region == "" {
		klog.Fatal("--region is required in provider mode")
	}

	backend, err := cfg.GetBackendForRegion(*region)
	if err != nil {
		klog.Fatal(err)
	}

	listenAddr := fmt.Sprintf(":%d", backend.Port)
	klog.Infof("starting provider mode for region=%s on %s; configured backends: %s", *region, listenAddr, cfg.Summary())

	server := newServer()
	provider := buildAWSCloudProvider(*region)
	var grpcService protos.CloudProviderServer
	grpcService = upstreamwrapper.NewCloudProviderGrpcWrapper(provider)
	if *dryRun {
		klog.Infof("dry-run mode enabled: mutating operations will be logged but not executed")
		grpcService = newDryRunWrapper(grpcService)
	}
	protos.RegisterCloudProviderServer(server, grpcService)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		klog.Fatalf("failed to listen on %q: %v", listenAddr, err)
	}

	klog.Infof("aws regional cloudprovider service (%s) listening on %s", *region, listenAddr)

	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	if err := server.Serve(listener); err != nil {
		klog.Fatalf("failed to serve gRPC API: %v", err)
	}
}

func runRouter(ctx context.Context, cfg *Config) {
	klog.Infof("starting router mode on %s; configured backends: %s", *routerAddress, cfg.Summary())

	router, err := newCachingRouter(cfg)
	if err != nil {
		klog.Fatalf("failed to create caching router: %v", err)
	}
	defer func() {
		if err := router.Close(); err != nil {
			klog.Errorf("failed to close router backend connections: %v", err)
		}
	}()
	router.Start(ctx)

	server := newServer()
	protos.RegisterCloudProviderServer(server, router)

	httpServer := newRouterHTTPServer(*httpAddress, router)
	serveHTTPServer(ctx, httpServer, "router HTTP server")

	listener, err := net.Listen("tcp", *routerAddress)
	if err != nil {
		klog.Fatalf("failed to listen on %q: %v", *routerAddress, err)
	}

	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	klog.Infof("caching router listening on %s", *routerAddress)
	if err := server.Serve(listener); err != nil {
		klog.Fatalf("failed to serve gRPC API: %v", err)
	}
}

func newServer() *grpc.Server {
	if *keyCert == "" || *cert == "" || *cacert == "" {
		klog.V(1).Info("TLS assets not provided, starting insecure gRPC server")
		return grpc.NewServer()
	}

	certificate, err := tls.LoadX509KeyPair(*cert, *keyCert)
	if err != nil {
		klog.Fatalf("failed to read TLS certificate pair: %v", err)
	}

	certPool := x509.NewCertPool()
	caBundle, err := os.ReadFile(*cacert)
	if err != nil {
		klog.Fatalf("failed to read client CA certificate: %v", err)
	}
	if ok := certPool.AppendCertsFromPEM(caBundle); !ok {
		klog.Fatal("failed to append client CA certificate")
	}

	transportCreds := credentials.NewTLS(&tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    certPool,
	})
	return grpc.NewServer(grpc.Creds(transportCreds))
}

func buildAWSCloudProvider(region string) cloudprovider.CloudProvider {
	opts := &coreoptions.AutoscalerOptions{
		AutoscalingOptions: config.AutoscalingOptions{
			CloudProviderName:        cloudprovider.AwsProviderName,
			CloudConfig:              *cloudConfig,
			NodeGroupAutoDiscovery:   *nodeGroupAutoDiscoveryFlag,
			NodeGroups:               *nodeGroupsFlag,
			ClusterName:              *clusterName,
			AWSUseStaticInstanceList: *awsUseStaticInstanceList,
			UserAgent:                "aws-cluster-autoscaler-external-grpc",
		},
	}

	discovery := cloudprovider.NodeGroupDiscoveryOptions{
		NodeGroupSpecs:              opts.NodeGroups,
		NodeGroupAutoDiscoverySpecs: opts.NodeGroupAutoDiscovery,
	}

	resourceLimiter := cloudprovider.NewResourceLimiter(nil, nil)

	return buildProviderForRegion(region, func() cloudprovider.CloudProvider {
		return upstreamaws.BuildAWS(opts, discovery, resourceLimiter)
	})
}
