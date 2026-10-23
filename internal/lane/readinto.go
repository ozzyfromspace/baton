package lane

import "os"

// ReadInto reads the file at path into r a line at a time, as a gate's output is read (Reading.Line): its
// ::baton fail lines and the failure lines of r's runners. A stage reads the side log its command writes
// its output to this way (`baton stage --log`), and FailureIDs a job's log.
func ReadInto(r *Reading, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	eachLine(f, r.Line)
	return nil
}
