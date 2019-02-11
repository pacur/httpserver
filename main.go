package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const body = `<html>
<head><title>Index of %s</title></head>
<body bgcolor="white">
<h1>Index of %s</h1><hr><pre><a href="../">../</a>
%s</pre><hr></body>
</html>
`

func IsDirectory(path string) (dir bool, err error) {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return
	}

	dir = stat.IsDir()
	return
}

type Item struct {
	Name      string
	IsDir     bool
	Formatted string
}

type Items struct {
	items []Item
}

func (s *Items) Len() (n int) {
	n = len(s.items)
	return
}

func (s *Items) Less(i int, j int) bool {
	iDir := s.items[i].IsDir
	jDir := s.items[j].IsDir
	iHidden := s.items[i].Name[:1] == "."
	jHidden := s.items[j].Name[:1] == "."

	if iDir && !jDir {
		return true
	} else if !iDir && jDir {
		return false
	} else if iHidden && !jHidden {
		return true
	} else if !iHidden && jHidden {
		return false
	}

	return s.items[i].Name < s.items[j].Name
}

func (s *Items) Swap(i int, j int) {
	s.items[i], s.items[j] = s.items[j], s.items[i]
}

func (s *Items) Add(item Item) {
	s.items = append(s.items, item)
}

func (s *Items) Sort() {
	sort.Sort(s)
}

func (s *Items) Join(sep string) (data string) {
	for i, item := range s.items {
		if i != 0 {
			data += sep
		}
		data += item.Formatted
	}
	return
}

type StaticHandler struct {
	Root        string
	Cache       bool
	ContentType string
	fileServer  http.Handler
}

func (h *StaticHandler) Handle(c *gin.Context) {
	if !h.Cache {
		c.Writer.Header().Add("Cache-Control",
			"no-cache, no-store, must-revalidate")
		c.Writer.Header().Add("Pragma", "no-cache")
		c.Writer.Header().Add("Expires", "0")
	}

	path := filepath.Join(h.Root, filepath.FromSlash(
		filepath.Clean("/"+c.Param("filepath"))))

	isDir, err := IsDirectory(path)
	if err != nil {
		c.AbortWithError(500, err)
		return
	}

	ok := false
	if isDir {
		ok, err = h.HandleDirList(path, c)
		if err != nil {
			c.AbortWithError(500, err)
			return
		}
	}

	if !ok {
		if h.ContentType != "" {
			c.Writer.Header().Add("Content-Type", h.ContentType)
		}
		h.fileServer.ServeHTTP(c.Writer, c.Request)
	}
}

func (h *StaticHandler) HandleDirList(path string, c *gin.Context) (
	ok bool, err error) {

	pathFrm := filepath.Clean("/" + c.Param("filepath"))
	if !strings.HasSuffix(pathFrm, "/") {
		pathFrm += "/"
	}

	if !strings.HasSuffix(c.Request.URL.Path, "/") {
		c.Redirect(301, c.Request.URL.Path+"/")
	}

	items := &Items{}

	itemsAll, err := ioutil.ReadDir(path)
	if err != nil {
		return
	}

	for _, item := range itemsAll {
		name := item.Name()
		if name == "index.html" {
			return
		}

		modTime := item.ModTime().Format("02-Jan-2006 15:04")

		if item.Mode()&os.ModeSymlink != 0 {
			linkPath, e := os.Readlink(filepath.Join(path, item.Name()))
			if e != nil {
				err = e
				return
			}

			itm, e := os.Lstat(linkPath)
			if e != nil {
				if os.IsNotExist(e) {
					continue
				}
				err = e
				return
			}
			item = itm
		}

		size := ""
		if item.IsDir() {
			name += "/"
			size = "-"
		} else {
			size = fmt.Sprintf("%d", item.Size())
		}

		formattedName := name
		if len(formattedName) > 50 {
			formattedName = formattedName[:47] + "..>"
		}

		items.Add(Item{
			Name:  name,
			IsDir: item.IsDir(),
			Formatted: fmt.Sprintf(
				`<a href="%s">`, name) + fmt.Sprintf(
				"%-54s %s %19s", formattedName+"</a>", modTime, size),
		})
	}

	items.Sort()

	ok = true
	data := []byte(fmt.Sprintf(body, pathFrm, pathFrm, items.Join("\n")))
	c.Data(200, "text/html", data)

	return
}

func (h *StaticHandler) Setup(engine *gin.Engine) {
	fs := gin.Dir(h.Root, false)
	h.fileServer = http.StripPrefix("/", http.FileServer(fs))

	engine.GET("/*filepath", h.Handle)
	engine.HEAD("/*filepath", h.Handle)

	return
}

func selfCert(parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (
	cert *x509.Certificate, certByt []byte, certKey *ecdsa.PrivateKey,
	err error) {

	certKey, err = ecdsa.GenerateKey(
		elliptic.P384(),
		rand.Reader,
	)
	if err != nil {
		return
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return
	}

	certTempl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Pacur HTTP Server"},
		},
		NotBefore: time.Now().Add(-24 * time.Hour),
		NotAfter:  time.Now().Add(26280 * time.Hour),
		KeyUsage: x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		SignatureAlgorithm:    x509.ECDSAWithSHA256,
	}

	if parent == nil {
		parent = certTempl
		parentKey = certKey
	}

	certByt, err = x509.CreateCertificate(rand.Reader, certTempl, parent,
		certKey.Public(), parentKey)
	if err != nil {
		return
	}

	cert, err = x509.ParseCertificate(certByt)
	if err != nil {
		return
	}

	return
}

func main() {
	path, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	pathPtr := flag.String("path", path, "Path to serve")
	hostPtr := flag.String("host", "[::]", "Server host")
	portPtr := flag.Int("port", 8000, "Server port number")
	cachePtr := flag.Bool("cache", false, "Enable cache")
	tlsServerPtr := flag.Bool("tls", false, "Enable TLS server")
	contentTypePtr := flag.String("type", "", "Force content type")
	flag.Parse()
	path = *pathPtr
	host := *hostPtr
	port := *portPtr
	cache := *cachePtr
	tlsServer := *tlsServerPtr
	contentType := *contentTypePtr

	path, err = filepath.Abs(path)
	if err != nil {
		panic(err)
	}

	static := &StaticHandler{
		Root:        path,
		Cache:       cache,
		ContentType: contentType,
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	static.Setup(router)

	scheme := ""
	if tlsServer {
		scheme = "https"
	} else {
		scheme = "http"
	}
	fmt.Printf("Listening and serving %s on %s://%s:%d\n",
		path, scheme, host, port)

	server := http.Server{
		Addr:    fmt.Sprintf("%s:%d", host, port),
		Handler: router,
	}

	if tlsServer {
		caCert, _, caKey, err := selfCert(nil, nil)
		if err != nil {
			panic(err)
		}

		_, certByt, certKey, err := selfCert(caCert, caKey)
		if err != nil {
			panic(err)
		}

		certKeyByte, err := x509.MarshalECPrivateKey(certKey)
		if err != nil {
			panic(err)
		}

		certKeyBlock := &pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: certKeyByte,
		}
		keyPem := pem.EncodeToMemory(certKeyBlock)

		certBlock := &pem.Block{
			Type:  "CERTIFICATE",
			Bytes: certByt,
		}
		certPem := pem.EncodeToMemory(certBlock)

		keypair, err := tls.X509KeyPair(certPem, keyPem)
		if err != nil {
			return
		}

		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS12,
			Certificates: []tls.Certificate{
				keypair,
			},
		}

		listener, err := tls.Listen("tcp", server.Addr, tlsConfig)
		if err != nil {
			panic(err)
			return
		}

		err = server.Serve(listener)
		if err != nil {
			panic(err)
		}
	} else {
		err = server.ListenAndServe()
		if err != nil {
			panic(err)
		}
	}
}
