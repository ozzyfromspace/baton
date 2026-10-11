package machine

import "os"

func read(r *Reading) error {
	return fromProc(os.DirFS("/"), r)
}
