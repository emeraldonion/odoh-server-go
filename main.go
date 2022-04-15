// The MIT License
//
// Copyright (c) 2019-2020, Cloudflare, Inc. and Apple, Inc. All rights reserved.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/cisco/go-hpke"
	"github.com/cloudflare/odoh-go"
	"github.com/jessevdk/go-flags"
	log "github.com/sirupsen/logrus"
)

const (
	// HPKE constants
	kemID  = hpke.DHKEM_X25519
	kdfID  = hpke.KDF_HKDF_SHA256
	aeadID = hpke.AEAD_AESGCM128

	// Keying material (seed) should have as many bits of entropy as the bit length of the x25519 secret key
	defaultSeedLength = 32
)

// Set by build process
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// CLI flags
var opts struct {
	ListenAddr        string  `short:"l" long:"listen" description:"Address to listen on" default:"localhost:8080"`
	MetricsListenAddr string  `short:"m" long:"metrics-listen" description:"Address to listen metrics server on" default:"localhost:8081"`
	Resolver          string  `short:"r" long:"resolver" description:"Target DNS resolver" default:"127.0.0.1:53"`
	DisableTls        bool    `short:"t" long:"no-tls" description:"Disable TLS"`
	Cert              string  `short:"c" long:"cert" description:"TLS certificate file"`
	Key               string  `short:"k" long:"key" description:"TLS key file"`
	ResolverTimeout   float32 `long:"resolver-timeout" description:"Resolver timeout (seconds)" default:"2.5"`
	ProxyTimeout      float32 `long:"proxy-timeout" description:"Proxy timeout (seconds)" default:"2.5"`
	Verbose           bool    `short:"v" long:"verbose" description:"Enable verbose logging"`
	ShowVersion       bool    `short:"V" long:"version" description:"Show version and exit"`
}

func keyPair() (*odoh.ObliviousDoHKeyPair, error) {
	// Random seed for HPKE keypair
	seed := make([]byte, defaultSeedLength)
	_, err := rand.Read(seed)
	if err != nil {
		return nil, err
	}

	kp, err := odoh.CreateKeyPairFromSeed(hpke.DHKEM_X25519, hpke.KDF_HKDF_SHA256, hpke.AEAD_AESGCM128, seed)
	if err != nil {
		return nil, err
	}

	return &kp, err
}

// serverPair returns a target and proxy server
func serverPair(keyPair odoh.ObliviousDoHKeyPair) (*targetServer, *proxyServer) {
	log.Debugf("resolver timeout: %+v", opts.ResolverTimeout)
	target := &targetServer{
		resolver: &targetResolver{
			timeout:    time.Duration(opts.ResolverTimeout) * time.Second,
			nameserver: opts.Resolver,
		},
		odohKeyPair: keyPair,
	}

	log.Debugf("proxy timeout: %+v", opts.ProxyTimeout)
	proxy := &proxyServer{
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 1024,
				TLSHandshakeTimeout: time.Duration(opts.ProxyTimeout) * time.Second,
			},
		},
	}

	return target, proxy
}

// setupHandlers configures HTTP handlers
func setupHandlers(target *targetServer, proxy *proxyServer) {
	http.HandleFunc("/proxy", proxy.proxyQueryHandler)
	http.HandleFunc("/dns-query", target.targetQueryHandler)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "ok") })
	http.HandleFunc("/.well-known/odohconfigs", target.configHandler)
}

// serve starts the HTTP server
func serve(listenAddr string, tls bool, tlsCert, tlsKey string) {
	// Start the server
	log.Infof("Starting ODoH listener on %s", listenAddr)
	if tls { // HTTPS listener
		log.Fatal(http.ListenAndServeTLS(listenAddr, tlsCert, tlsKey, nil))
	} else { // HTTP listener
		log.Fatal(http.ListenAndServe(listenAddr, nil))
	}
}

func main() {
	// Parse cli flags
	_, err := flags.ParseArgs(&opts, os.Args)
	if err != nil {
		os.Exit(1)
	}

	// Enable debug logging in development releases
	if //noinspection GoBoolExpressions
	version == "dev" || opts.Verbose {
		log.SetLevel(log.DebugLevel)
		log.Debugln("Verbose logging enabled")
	}

	if opts.ShowVersion {
		log.Printf("odohd %s commit %s date %s\n", version, commit, date)
		os.Exit(0)
	}

	// Validate TLS cert/key
	if !opts.DisableTls && (opts.Cert == "" || opts.Key == "") {
		log.Fatal("--cert and --key must be set when TLS is enabled")
	}

	// Start metrics server
	go func() {
		log.Infof("Starting metrics server on %s", opts.MetricsListenAddr)
		log.Fatal(metricsServe(opts.MetricsListenAddr))
	}()

	kp, err := keyPair()
	if err != nil {
		log.Fatal(err)
	}

	proxy, target := serverPair(*kp)
	setupHandlers(proxy, target)
	serve(opts.ListenAddr, !opts.DisableTls, opts.Cert, opts.Key)
}
