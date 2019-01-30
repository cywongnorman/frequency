package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/securecookie"
	"golang.org/x/crypto/acme/autocert"
)

var (
	// Flags
	cli = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	// datadir
	datadir string

	// The version is set by the build command.
	version string

	// FQDN
	httpHost string

	// backlink
	backlink string

	// show version
	showVersion bool

	// show help
	showHelp bool

	// debug logging
	debug bool

	// delete old files
	deleteOldFiles bool

	// compress old files
	compressOldFiles bool

	// HTTP read limit
	httpReadLimit int64 = 2 * (1024 * 1024)

	// securetoken
	securetoken *securecookie.SecureCookie

	// logger
	logger = log.New()

	// config
	config *Config

	// mailer
	mailer = NewMailer()

	// Error page HTML
	errorPageHTML = `<html><head><title>Error</title></head><body text="orangered" bgcolor="black"><h1>An error has occurred</h1></body></html>`

	cpuprofile string
	memprofile string

	sigint chan os.Signal
)

func init() {
	cli.StringVar(&datadir, "datadir", "/data", "data dir")
	cli.StringVar(&backlink, "backlink", "", "backlink (optional)")
	cli.StringVar(&httpHost, "http-host", "", "HTTP host")
	cli.BoolVar(&showVersion, "version", false, "display version and exit")
	cli.BoolVar(&showHelp, "help", false, "display help and exit")
	cli.BoolVar(&debug, "debug", false, "debug mode")
	cli.BoolVar(&deleteOldFiles, "delete-old-files", true, "delete oldest files when storage exceeds 95% full")
	cli.BoolVar(&compressOldFiles, "compress-old-files", false, "compress files for past days")
	cli.StringVar(&cpuprofile, "cpuprofile", "", "write cpu profile to `file`")
	cli.StringVar(&memprofile, "memprofile", "", "write mem profile to `file`")
}

func main() {
	var err error

	cli.Parse(os.Args[1:])
	usage := func(msg string) {
		if msg != "" {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", msg)
		}
		fmt.Fprintf(os.Stderr, "Usage: %s --http-host analytics.example.com\n\n", os.Args[0])
		cli.PrintDefaults()
	}

	if showHelp {
		usage("Help info")
		os.Exit(0)
	}

	if showVersion {
		fmt.Printf("Frequency Analytics %s\n", version)
		os.Exit(0)
	}

	// http host
	if httpHost == "" {
		usage("the --http-host flag is required")
		os.Exit(1)
	}

	// debug logging
	logger.Out = os.Stdout
	if debug {
		logger.SetLevel(log.DebugLevel)
	}
	logger.Debugf("debug logging is enabled")

	// Handle SIGINT
	sigint = make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt)
	go func() {
		<-sigint
		if cpuprofile != "" {
			pprof.StopCPUProfile()
		}
		os.Exit(0)
	}()

	// Run file manager.
	go fileman()

	if cpuprofile != "" {
		f, err := os.Create(cpuprofile)
		if err != nil {
			logger.Fatalf("could not create CPU profile: %s", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			logger.Fatalf("could not start CPU profile: %s", err)
		}
		defer pprof.StopCPUProfile()
	}

	if memprofile != "" {
		f, err := os.Create(memprofile)
		if err != nil {
			logger.Fatalf("could not create memory profile: %s", err)
		}
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			logger.Fatalf("could not write memory profile: %s", err)
		}
		f.Close()
	}

	// config
	config, err = NewConfig("config.json")
	if err != nil {
		logger.Fatal(err)
	}

	// Secure token
	securetoken = securecookie.New([]byte(config.FindInfo().HashKey), []byte(config.FindInfo().BlockKey))

	//
	// Routes
	//
	r := &httprouter.Router{}
	r.GET("/", Log(WebHandler(indexHandler, "index")))
	r.GET("/analytics.js", WebHandler(analyticsHandler, "analytics"))
	r.GET("/ping", WebHandler(pingHandler, "ping"))

	r.GET("/configure", Log(WebHandler(configureHandler, "configure")))
	r.POST("/configure", Log(WebHandler(configureHandler, "configure")))

	r.GET("/forgot", Log(WebHandler(forgotHandler, "forgot")))
	r.POST("/forgot", Log(WebHandler(forgotHandler, "forgot")))

	r.GET("/signin", Log(WebHandler(signinHandler, "signin")))
	r.POST("/signin", Log(WebHandler(signinHandler, "signin")))

	r.GET("/signout", Log(WebHandler(signoutHandler, "signout")))

	r.GET("/settings", Log(WebHandler(settingsHandler, "settings")))
	r.POST("/settings", Log(WebHandler(settingsHandler, "settings")))

	r.GET("/help", Log(WebHandler(helpHandler, "help")))

	// Properties
	r.GET("/property/add", Log(WebHandler(addPropertyHandler, "property/add")))
	r.POST("/property/add", Log(WebHandler(addPropertyHandler, "property/add")))
	r.GET("/property/dashboard/:property", Log(WebHandler(dashboardPropertyHandler, "property/dashboard")))
	r.POST("/property/dashboard", Log(WebHandler(dashboardPropertyHandler, "property/dashboard")))
	r.GET("/property/snippet/:property", Log(WebHandler(snippetPropertyHandler, "property/snippet")))

	r.GET("/property/sources/:property", Log(WebHandler(sourcesPropertyHandler, "property/sources")))
	r.POST("/property/sources", Log(WebHandler(sourcesPropertyHandler, "property/sources")))

	r.GET("/property/pages/:property", Log(WebHandler(pagesPropertyHandler, "property/pages")))
	r.POST("/property/pages", Log(WebHandler(pagesPropertyHandler, "property/pages")))

	r.GET("/property/referrers/:property", Log(WebHandler(referrersPropertyHandler, "property/referrers")))
	r.POST("/property/referrers", Log(WebHandler(referrersPropertyHandler, "property/referrers")))

	r.GET("/property/platforms/:property", Log(WebHandler(platformsPropertyHandler, "property/platforms")))
	r.POST("/property/platforms", Log(WebHandler(platformsPropertyHandler, "property/platforms")))

	r.GET("/property/events/:property", Log(WebHandler(eventsPropertyHandler, "property/events")))
	r.POST("/property/events", Log(WebHandler(eventsPropertyHandler, "property/events")))

	/* r.GET("/property/chart/pageview/:property", Log(WebHandler(pageviewChartHandler, "property/chart/pageview"))) */

	r.GET("/property/settings/:property", Log(WebHandler(settingsPropertyHandler, "property/settings")))
	r.POST("/property/settings", Log(WebHandler(settingsPropertyHandler, "property/settings")))

	r.GET("/property/delete/:property", Log(WebHandler(deletePropertyHandler, "property/delete")))
	r.POST("/property/delete", Log(WebHandler(deletePropertyHandler, "property/delete")))

	// Static assets
	r.GET("/static/*path", staticHandler)

	//
	// Server
	//

	httpTimeout := 10 * time.Minute
	maxHeaderBytes := 10 * (1024 * 1024)

	// autocert
	certmanager := autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(filepath.Join(datadir, "letsencrypt")),
		HostPolicy: func(_ context.Context, host string) error {
			host = strings.TrimPrefix(host, "www.")
			if host == httpHost {
				return nil
			}
			if host == config.FindInfo().Domain {
				return nil
			}
			return fmt.Errorf("autocert: host %q not permitted by HostPolicy", host)
		},
	}

	// http redirect to https and Let's Encrypt auth
	go func() {
		redir := httprouter.New()
		redir.GET("/*path", func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
			r.URL.Scheme = "https"
			r.URL.Host = httpHost
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
		})

		httpd := &http.Server{
			Handler:        certmanager.HTTPHandler(redir),
			Addr:           ":80",
			WriteTimeout:   httpTimeout,
			ReadTimeout:    httpTimeout,
			MaxHeaderBytes: maxHeaderBytes,
		}
		if err := httpd.ListenAndServe(); err != nil {
			logger.Fatalf("http server on port 80 failed: %s", err)
		}
	}()

	// TLS
	tlsConfig := tls.Config{
		GetCertificate: certmanager.GetCertificate,
		NextProtos:     []string{"http/1.1"},
		Rand:           rand.Reader,
		PreferServerCipherSuites: true,
		MinVersion:               tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,

			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}

	httpsd := &http.Server{
		Handler:        r,
		Addr:           ":443",
		WriteTimeout:   httpTimeout,
		ReadTimeout:    httpTimeout,
		MaxHeaderBytes: maxHeaderBytes,
	}

	// Enable TCP keep alives on the TLS connection.
	tcpListener, err := net.Listen("tcp", ":443")
	if err != nil {
		logger.Fatalf("listen failed: %s", err)
		return
	}
	tlsListener := tls.NewListener(tcpKeepAliveListener{tcpListener.(*net.TCPListener)}, &tlsConfig)

	logger.Infof("Frequency Analytics %s %s", version, &url.URL{
		Scheme: "https",
		Host:   httpHost,
		Path:   "/",
	})
	logger.Fatal(httpsd.Serve(tlsListener))
}

type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (l tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := l.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(10 * time.Minute)
	return tc, nil
}
