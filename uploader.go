package main

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

/**
  Uploader is a special type of Http server.
  Put any config state in here.
  The point of this server is to show how
  upload and download can be extremely efficient
  for large files.
*/
type uploader struct {
	HomeBucket   string
	Port         int
	Bind         string
	Addr         string
	UploadCookie string
	BufferSize   int
	Key          []byte
	IV           [aes.BlockSize]byte
}

// CountingStreamReader takes statistics as it writes
type CountingStreamReader struct {
	S cipher.Stream
	R io.Reader
}

// Read takes statistics as it writes
func (r CountingStreamReader) Read(dst []byte) (n int, err error) {
	n, err = r.R.Read(dst)
	r.S.XORKeyStream(dst[:n], dst[:n])
	return
}

// CountingStreamWriter keeps statistics as it writes
type CountingStreamWriter struct {
	S     cipher.Stream
	W     io.Writer
	Error error
}

func (w CountingStreamWriter) Write(src []byte) (n int, err error) {
	c := make([]byte, len(src))
	w.S.XORKeyStream(c, src)
	n, err = w.W.Write(c)
	if n != len(src) {
		if err == nil {
			err = io.ErrShortWrite
		}
	}
	return
}

// Close closes underlying stream
func (w CountingStreamWriter) Close() error {
	if c, ok := w.W.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func doCipherByReaderWriter(inFile io.Reader, outFile io.Writer, key []byte, iv [aes.BlockSize]byte) error {
	writeCipher, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	writeCipherStream := cipher.NewCTR(writeCipher, iv[:])
	if err != nil {
		return err
	}

	reader := &CountingStreamReader{S: writeCipherStream, R: inFile}
	_, err = io.Copy(outFile, reader)

	return err
}

/**
  Uploader has a function to drain an http request off to a filename
  Note that writing to a file is not the only possible course of action.
  The part name (or file name, content type, etc) may insinuate that the file
  is small, and should be held in memory.
*/
func (h uploader) serveHTTPUploadPOSTDrain(fileName string, w http.ResponseWriter, part *multipart.Part) error {
	log.Printf("read part %s", fileName)
	drainTo, drainErr := os.Create(fileName)
	if drainErr != nil {
		log.Printf("error draining file: %v", drainErr)
	}
	defer drainTo.Close()

	return doCipherByReaderWriter(part, drainTo, h.Key, h.IV)
}

/**
  Uploader retrieve a form for doing uploads.

  Serve up an example form.  There is nothing preventing
  a client from deciding to send us a POST with 1000
  1Gb to 64Gb files in them.  That would be something like
  S3 bucket uploads.

  We can make it a matter of specification that headers larger
  than this must fail.  But for the multi-part mime chunks,
  we must handle files larger than memory.
*/
func (h uploader) serveHTTPUploadGETMsg(msg string, w http.ResponseWriter, r *http.Request) {
	log.Print("get an upload get")
	r.Header.Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html>")
	fmt.Fprintf(w, "<head>")
	fmt.Fprintf(w, "<title>Upload A File</title>")
	fmt.Fprintf(w, "</head>")
	fmt.Fprintf(w, "<body>")
	fmt.Fprintf(w, msg+"<br>")
	fmt.Fprintf(w, "<form action='/upload' method='POST' enctype='multipart/form-data'>")
	fmt.Fprintf(w, "<input type='hidden' value='"+h.UploadCookie+"' name='uploadCookie'>")
	fmt.Fprintf(w, "The File: <input name='theFile' type='file'>")
	fmt.Fprintf(w, "<input type='submit'>")
	fmt.Fprintf(w, "</form>")
	fmt.Fprintf(w, "</body>")
	fmt.Fprintf(w, "</html>")
}

/**
  Check a value against a bounded(!) buffer
*/
func valCheck(buffer []byte, refVal []byte, checkedVal *multipart.Part) bool {
	totalBytesRead := 0
	bufferLength := len(buffer)
	for {
		if totalBytesRead >= bufferLength {
			break
		}
		bytesRead, err := checkedVal.Read(buffer[totalBytesRead:])
		if bytesRead < 0 || err == io.EOF {
			break
		}
		totalBytesRead += bytesRead
	}

	i := 0
	refValLength := len(refVal)
	if totalBytesRead != refValLength {
		return false
	}
	for i < refValLength {
		if refVal[i] != buffer[i] {
			return false
		}
		i++
	}

	return true

}

func (h uploader) checkUploadCookie(part *multipart.Part) bool {
	//We must do a BOUNDED read of the cookie.  Just let it fail if it's not < 8k
	buffer := make([]byte, h.BufferSize)
	uploadCookieBytes := []byte(h.UploadCookie)
	return valCheck(buffer, uploadCookieBytes, part)
}

/**
  Demonstrate efficient uploading in the face of any
  crazy request we get.  We can use heuristics such as
  the names of parts to DECIDE whether it's reasonable to
  put the data into memory (json metadata), or to create a
  file handle to drain it off, or to start off in memory
  and then drain it off somewhere if it becomes unreasonably
  large (may be useful for being optimally efficient).
  This is the key to scalability, because we have
  full control over handling HTTP.

  If we have an SLA to handle a certain number of connections,
  putting an upper bound on memory usage per session lets us
  have such a guarantee, where we can use admission control (TBD)
  to limit the number of sessions to amounts within the SLA
  to ensure that sessions started can complete without interference
  from sessions that are doomed to fail from congestion.
*/
func (h uploader) serveHTTPUploadPOST(w http.ResponseWriter, r *http.Request) {
	multipartReader, err := r.MultipartReader()
	if err != nil {
		log.Printf("failed to get a multipart reader %v", err)
		http.Error(w, "failed to get a multipart reader", 500)
		return
	}

	isAuthorized := false
	for {
		//DOS problem .... what if this header is very large?  (Intentionally)
		part, partErr := multipartReader.NextPart()
		if partErr != nil {
			if partErr == io.EOF {
				break //just an eof...not an error
			} else {
				log.Printf("error getting a part %v", partErr)
				http.Error(w, "error getting a part", 500)
				return
			}
		} else {
			if strings.Compare(part.FormName(), "uploadCookie") == 0 {
				if h.checkUploadCookie(part) {
					isAuthorized = true
				}
			} else {
				if len(part.FileName()) > 0 {
					if isAuthorized {
						fileName := h.HomeBucket + "/" + part.FileName()
						//Could take an *indefinite* amount of time!!
						err := h.serveHTTPUploadPOSTDrain(fileName, w, part)
						if err != nil {
							log.Printf("error draining part: %v", err)
						}
					} else {
						log.Printf("failed authorization for file")
						http.Error(w, "failed authorization for file", 400)
						return
					}
				}
			}
		}
	}
	h.serveHTTPUploadGETMsg("ok", w, r)
}

/**
Uploader method to show a form with no status from previous upload
*/
func (h uploader) serveHTTPUploadGET(w http.ResponseWriter, r *http.Request) {
	h.serveHTTPUploadGETMsg("", w, r)
}

/**
Efficiently retrieve a file
*/
func (h uploader) serveHTTPDownloadGET(w http.ResponseWriter, r *http.Request) {
	fileName := h.HomeBucket + "/" + r.URL.RequestURI()[len("/download/"):]
	log.Printf("download request for %s", fileName)
	downloadFrom, err := os.Open(fileName)
	if err != nil {
		log.Print("failed to open file for reading")
		http.Error(w, "failed to open file for reading", 500)
		return
	}
	defer downloadFrom.Close()
	doCipherByReaderWriter(downloadFrom, w, h.Key, h.IV)
}

/**
  Handle command routing explicitly.
*/
func (h uploader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Compare(r.URL.RequestURI(), "/upload") == 0 {
		if strings.Compare(r.Method, "GET") == 0 {
			h.serveHTTPUploadGET(w, r)
		} else {
			if strings.Compare(r.Method, "POST") == 0 {
				h.serveHTTPUploadPOST(w, r)
			}
		}
	} else {
		if strings.HasPrefix(r.URL.RequestURI(), "/download/") {
			h.serveHTTPDownloadGET(w, r)
		}
	}
}

/**
Generate a simple server in the root that we specify.
We assume that the directory may not exist, and we set permissions
on it
*/
func makeServer(
	theRoot string,
	bind string,
	port int,
	uploadCookie string,
) *http.Server {
	//Just ensure that this directory exists
	os.Mkdir(theRoot, 0700)
	h := uploader{
		HomeBucket:   theRoot,
		Port:         port,
		Bind:         bind,
		UploadCookie: uploadCookie,
		BufferSize:   1024 * 8, //Each session takes a buffer that guarantees the number of sessions in our SLA
	}
	h.Addr = h.Bind + ":" + strconv.Itoa(h.Port)
	h.Key = []byte("asdfaddsfadfasdf2543654321546788")

	//A web server is running
	return &http.Server{
		Addr:           h.Addr,
		Handler:        h,
		ReadTimeout:    10000 * time.Second, //This breaks big downloads
		WriteTimeout:   10000 * time.Second,
		MaxHeaderBytes: 1 << 20, //This prevents clients from DOS'ing us
	}
}

/**
  Use the lowest level of control for creating the Server
  so that we know what all of the options are.

  Timeouts really should handled in the URL handler.
  Timeout should be based on lack of progress,
  rather than total time (ie: should active telnet sessions die based on time?),
  because large files just take longer.
*/
func main() {
	s := makeServer("/tmp/uploader", "127.0.0.1", 6060, "y0UMayUpL0Ad")
	log.Printf("open a browser at: %s", "https://"+s.Addr+"/upload")
	log.Fatal(s.ListenAndServeTLS("cert.pem", "key.pem"))
}
