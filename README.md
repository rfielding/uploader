# Large File Uploader

This is a project to make a simple Go uploader that can deal
with the largest files, by using multipart mime protocol
correctly (almost nothing does).  This means that buffering
in memory is bounded.

It is possible to have a hybrid that keeps a reasonable amount of
data in memory without ever writing it to disk, and flushes
to disk when a session is going to begin to use unfair amounts
of memory



Browser:

* run ./gencerts so that the SSL server can launch
* go run uploader.go
* by default it uses /tmp/uploader, a directory that should exist
* http://localhost:6060/upload   (pick some file, like foo.txt)
* http://localhost:6060/download/foo.txt  (assuming you uploaded it)
