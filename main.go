package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

/**
  This is a special type of Http server.
  Put any config state in here.
  The point of this server is to show how
  upload and download can be extremely efficient
  for large files.
*/
type Uploader struct {
	HomeBucket string
}

/**
  Note that writing to a file is not the only possible course of action.
  The part name (or file name, content type, etc) may insinuate that the file
  is small, and should be held in memory.
*/
func ServeHTTPUploadPOSTDrain(fileName string, w http.ResponseWriter, part *multipart.Part) {
	log.Printf("read part %s", fileName)
	//Dangerous... Should whitelist char names to prevent writes
	//outside the homeBucket!
	drainTo, drainErr := os.Create(fileName)
	defer drainTo.Close()

	if drainErr != nil {
		log.Printf("cannot write out file %s, %v", fileName, drainErr)
		http.Error(w, "cannot write out file", 500)
		return
	}

	drain := bufio.NewWriter(drainTo)
	var bytesWritten = 0
	var lastBytesRead = 0
	buffer := make([]byte, 1024*8)
	for lastBytesRead >= 0 {
		bytesRead, berr := part.Read(buffer)
		lastBytesRead = bytesRead
		if berr == io.EOF {
			break
		}
		if berr != nil {
			log.Printf("error reading data! %v", berr)
			http.Error(w, "error reading data", 500)
			return
		}
		if lastBytesRead > 0 {
			bytesWritten += lastBytesRead
			drain.Write(buffer[:bytesRead])
			drain.Flush()
		}
	}
	log.Printf("wrote file %s of length %d", fileName, bytesWritten)
}

/**
  Serve up an example form.  There is nothing preventing
  a client from deciding to send us a POST with 1000
  1Gb to 64Gb files in them.  That would be something like
  S3 bucket uploads.

  We can make it a matter of specification that headers larger
  than this must fail.  But for the multi-part mime chunks,
  we must handle files larger than memory.
*/
func (h Uploader) ServeHTTPUploadGETMsg(msg string, w http.ResponseWriter, r *http.Request) {
	log.Print("get an upload get")
	r.Header.Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html>")
	fmt.Fprintf(w, "<head>")
	fmt.Fprintf(w, "<title>Upload A File</title>")
	fmt.Fprintf(w, "</head>")
	fmt.Fprintf(w, "<body>")
	fmt.Fprintf(w, msg+"<br>")
	fmt.Fprintf(w, "<form action='/upload' method='POST' enctype='multipart/form-data'>")
	fmt.Fprintf(w, "The File: <input name='theFile' type='file'>")
	fmt.Fprintf(w, "<input type='hidden' name='uploadCookie' value='youCanUpl0AD'>")
	fmt.Fprintf(w, "<input type='submit'>")
	fmt.Fprintf(w, "</form>")
	fmt.Fprintf(w, "</body>")
	fmt.Fprintf(w, "</html>")
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
func (h Uploader) ServeHTTPUploadPOST(w http.ResponseWriter, r *http.Request) {
	log.Print("handling an upload post")
	multipartReader, err := r.MultipartReader()

	if err != nil {
		log.Printf("failed to get a multipart reader %v", err)
		http.Error(w, "failed to get a multipart reader", 500)
		return
	}

	for {
		part, partErr := multipartReader.NextPart()
		if partErr != nil {
			if partErr == io.EOF || part == nil {
				break //just an eof...not an error
			} else {
				log.Printf("error getting a part %v", partErr)
				http.Error(w, "error getting a part", 500)
				return
			}
		} else {
			if len(part.FileName()) > 0 {
				fileName := h.HomeBucket + "/" + part.FileName()
				//Could take an *indefinite* amount of time!!
				ServeHTTPUploadPOSTDrain(fileName, w, part)
			}
		}
	}
	h.ServeHTTPUploadGETMsg("ok", w, r)
}

func (h Uploader) ServeHTTPUploadGET(w http.ResponseWriter, r *http.Request) {
	h.ServeHTTPUploadGETMsg("", w, r)
}

func (h Uploader) ServeHTTPDownloadGET(w http.ResponseWriter, r *http.Request) {
	fileName := h.HomeBucket + "/" + r.URL.RequestURI()[len("/download/"):]
	log.Printf("download request for %s", fileName)
	downloadFrom, err := os.Open(fileName)
	if err != nil {
		log.Print("failed to open file for reading")
		http.Error(w, "failed to open file for reading", 500)
		return
	}
	var bytesWritten = 0
	var lastBytesRead = 0
	buffer := make([]byte, 1024*8)
	for lastBytesRead >= 0 {
		bytesRead, berr := downloadFrom.Read(buffer)
		lastBytesRead = bytesRead
		if berr == io.EOF {
			break
		}
		if berr != nil {
			log.Printf("error reading data! %v", berr)
			http.Error(w, "error reading data", 500)
			return
		}
		if lastBytesRead > 0 {
			bytesWritten += lastBytesRead
			w.Write(buffer[:bytesRead])
		}
	}
	log.Printf("returned file %s of length %d", fileName, bytesWritten)
}

/**
  Handle command routing explicitly.
*/
func (h Uploader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Compare(r.URL.RequestURI(), "/upload") == 0 {
		if strings.Compare(r.Method, "GET") == 0 {
			h.ServeHTTPUploadGET(w, r)
		} else {
			if strings.Compare(r.Method, "POST") == 0 {
				h.ServeHTTPUploadPOST(w, r)
			}
		}
	} else {
		if strings.HasPrefix(r.URL.RequestURI(), "/download/") {
			h.ServeHTTPDownloadGET(w, r)
		}
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
	s := &http.Server{
		Addr: ":6060",
		Handler: Uploader{
			HomeBucket: "/tmp/uploader",
		},
		ReadTimeout:    10000 * time.Second, //I don't like this
		WriteTimeout:   10000 * time.Second, //Do not like
		MaxHeaderBytes: 1 << 20,             //This prevents clients from DOS'ing us
	}

	log.Printf("open a browser at: %s", "http://localhost"+s.Addr+"/upload")
	log.Fatal(s.ListenAndServe())
}
