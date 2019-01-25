package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // register in DefaultServerMux
	"os"
	"time"
	"encoding/json"

	"crypto/tls"

	"github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	"github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/dshuffma-ibm/grpc-web/go/grpcweb"
	"github.com/mwitkow/go-conntrack"
	"github.com/mwitkow/grpc-proxy/proxy"
	//"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"golang.org/x/net/context"
	_ "golang.org/x/net/trace" // register in DefaultServerMux
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

var (
	flagBindAddr    = pflag.String("server_bind_address", "0.0.0.0", "address to bind the server to")
	flagHttpPort    = pflag.Int("server_http_debug_port", 8080, "TCP port to listen on for HTTP1.1 debug calls.")
	flagHttpTlsPort = pflag.Int("server_http_tls_port", 8443, "TCP port to listen on for HTTPS (gRPC, gRPC-Web).")

	runHttpServer = pflag.Bool("run_http_server", true, "whether to run HTTP server")
	runTlsServer  = pflag.Bool("run_tls_server", true, "whether to run TLS server")

	useWebsockets = pflag.Bool("use_websockets", false, "whether to use beta websocket transport layer")

	flagHttpMaxWriteTimeout = pflag.Duration("server_http_max_write_timeout", 10*time.Second, "HTTP server config, max write duration.")
	flagHttpMaxReadTimeout  = pflag.Duration("server_http_max_read_timeout", 10*time.Second, "HTTP server config, max read duration.")
)

func main() {
	pflag.Parse()

	logrus.SetOutput(os.Stdout)

	logEntry := logrus.NewEntry(logrus.StandardLogger())

	grpcServer := buildGrpcProxyServer(logEntry)
	errChan := make(chan error)

	options := []grpcweb.Option{
		// gRPC-Web compatibility layer with CORS configured to accept on every request
		grpcweb.WithCorsForRegisteredEndpointsOnly(false),
	}
	if *useWebsockets {
		logrus.Println("using websockets")
		options = append(
			options,
			grpcweb.WithWebsockets(true),
			grpcweb.WithWebsocketOriginFunc(func(req *http.Request) bool {
				return true
			}),
		)
	}
	wrappedGrpc := grpcweb.WrapServer(grpcServer, options...)

	if !*runHttpServer && !*runTlsServer {
		logrus.Fatalf("Both run_http_server and run_tls_server are set to false. At least one must be enabled for grpcweb proxy to function correctly.")
	}

	http.Handle("/", wrappedGrpc)
	http.HandleFunc("/settings", leakSettings)

	if *runHttpServer {
		// Debug server.
		debugServer := buildServer(http.DefaultServeMux)
		//http.Handle("/", wrappedGrpc)
		//http.Handle("/metrics", promhttp.Handler())

		debugListener := buildListenerOrFail("http", *flagHttpPort)
		serveServer(debugServer, debugListener, "http", errChan)
	}

	if *runTlsServer {
		// tls server.
		/*servingServer := buildServer(wrappedGrpc)
		servingListener := buildListenerOrFail("http", *flagHttpTlsPort)
		servingListener = tls.NewListener(servingListener, buildServerTlsOrFail())
		serveServer(servingServer, servingListener, "http_tls", errChan)
		*/

		// tls server.
		servingServer := buildServer(http.DefaultServeMux)

		servingListener := buildListenerOrFail("http", *flagHttpTlsPort)
		servingListener = tls.NewListener(servingListener, buildServerTlsOrFail())
		serveServer(servingServer, servingListener, "http_tls", errChan)
	}



	<-errChan
	// TODO(mwitkow): Add graceful shutdown.
}

func leakSettings(w http.ResponseWriter, r *http.Request) {
	type Settings struct {
		BackendAddr string `json:"backend_addr"`
		BackendTLS bool `json:"backend_tls"`
		BackendTLSNoVerify bool `json:"backend_tls_no_verify"`
		BackendMaxCallRecvMsgSize int `json:"backend_max_call_recv_msg_size_bytes"`
		ExternalAddr string `json:"external_addr"`
		RunTLSServer bool `json:"run_tls_server"`
		RunHTTPServer bool `json:"run_http_server"`
		UseWebSockets bool `json:"use_websockets"`
		ServerHttpMaxWriteTimeout time.Duration `json:"server_http_max_write_timeout_ns"`
		ServerHttpMaxReadTimeout time.Duration `json:"server_http_max_read_timeout_ns"`
		KeepAliveClientInterval time.Duration `json:"keep_alive_client_interval_ns"`
		KeepAliveClientTimeout time.Duration `json:"keep_alive_client_timeout_ns"`
	}
	var theSettings Settings
	theSettings.BackendAddr = *flagBackendHostPort
	theSettings.BackendTLS = *flagBackendIsUsingTls
	theSettings.BackendTLSNoVerify = *flagBackendTlsNoVerify
	theSettings.BackendMaxCallRecvMsgSize = *flagMaxCallRecvMsgSize
	theSettings.RunTLSServer = *runTlsServer
	theSettings.RunHTTPServer = *runHttpServer
	theSettings.UseWebSockets = *useWebsockets
	theSettings.ServerHttpMaxWriteTimeout = *flagHttpMaxWriteTimeout
	theSettings.ServerHttpMaxReadTimeout = *flagHttpMaxReadTimeout
	theSettings.KeepAliveClientInterval = *flagKeepAliveClientInterval
	theSettings.KeepAliveClientTimeout = *flagKeepAliveClientTimeout
	theSettings.ExternalAddr = *flagExternalHostPort

	if theSettings.ExternalAddr == "" {
		theSettings.ExternalAddr = theSettings.BackendAddr // if external doesn't exist, show internal address
	}

	jsonData, _ := json.Marshal(theSettings)

	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonData)
}

//func buildServer(wrappedGrpc *grpcweb.WrappedGrpcServer) *http.Server {
func buildServer(handler http.Handler) *http.Server {
	return &http.Server{
		WriteTimeout: *flagHttpMaxWriteTimeout,
		ReadTimeout:  *flagHttpMaxReadTimeout,
		/*Handler: http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			wrappedGrpc.ServeHTTP(resp, req)
		}),*/
		Handler:      handler,
	}
}
/*
func buildServer2() *http.Server {
	return &http.Server{
		WriteTimeout: *flagHttpMaxWriteTimeout,
		ReadTimeout:  *flagHttpMaxReadTimeout,
		Handler: http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			//wrappedGrpc.ServeHTTP(resp, req)
			http.DefaultServeMux.ServeHTTP(resp, req)
		}),
	}
}*/

func serveServer(server *http.Server, listener net.Listener, name string, errChan chan error) {
	go func() {
		logrus.Infof("listening for %s on: %v", name, listener.Addr().String())
		if err := server.Serve(listener); err != nil {
			errChan <- fmt.Errorf("%s server error: %v", name, err)
		}
	}()
}

func buildGrpcProxyServer(logger *logrus.Entry) *grpc.Server {
	// gRPC-wide changes.
	grpc.EnableTracing = true
	grpc_logrus.ReplaceGrpcLogger(logger)

	// gRPC proxy logic.
	backendConn := dialBackendOrFail()
	director := func(ctx context.Context, fullMethodName string) (context.Context, *grpc.ClientConn, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		outCtx, _ := context.WithCancel(ctx)
		mdCopy := md.Copy()
		delete(mdCopy, "user-agent")
		outCtx = metadata.NewOutgoingContext(outCtx, mdCopy)
		return outCtx, backendConn, nil
	}
	// Server with logging and monitoring enabled.
	return grpc.NewServer(
		grpc.CustomCodec(proxy.Codec()), // needed for proxy to function.
		grpc.UnknownServiceHandler(proxy.TransparentHandler(director)),
		grpc_middleware.WithUnaryServerChain(
			grpc_logrus.UnaryServerInterceptor(logger),
			grpc_prometheus.UnaryServerInterceptor,
		),
		grpc_middleware.WithStreamServerChain(
			grpc_logrus.StreamServerInterceptor(logger),
			grpc_prometheus.StreamServerInterceptor,
		),
	)
}

func buildListenerOrFail(name string, port int) net.Listener {
	addr := fmt.Sprintf("%s:%d", *flagBindAddr, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed listening for '%v' on %v: %v", name, port, err)
	}
	return conntrack.NewListener(listener,
		conntrack.TrackWithName(name),
		conntrack.TrackWithTcpKeepAlive(20*time.Second),
		conntrack.TrackWithTracing(),
	)
}
