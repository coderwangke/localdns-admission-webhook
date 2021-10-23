package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"github.com/coderwangke/localdns-admission-webhook/pkg/webhook"
	"k8s.io/klog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

var port int
var certFile, keyFile string

func main() {
	flag.IntVar(&port, "port", 443, "Webhook server port.")
	flag.StringVar(&certFile, "tlsCertFile", "/etc/webhook/certs/cert.pem", "File containing the x509 Certificate for HTTPS.")
	flag.StringVar(&keyFile, "tlsKeyFile", "/etc/webhook/certs/key.pem", "File containing the x509 private key to --tlsCertFile.")
	flag.Parse()

	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		klog.Errorf("Filed to load key pair: %v", err)
	}

	whserver := &webhook.WebhookServer{
		Server: &http.Server{
			Addr:      fmt.Sprintf(":%v", port),
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", whserver.Serve)
	whserver.Server.Handler = mux

	go func() {
		if err := whserver.Server.ListenAndServeTLS("", ""); err != nil {
			klog.Errorf("Filed to listen and serve webhook server: %v", err)
		}
	}()

	// listening OS shutdown singal
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	whserver.Server.Shutdown(context.Background())
}
