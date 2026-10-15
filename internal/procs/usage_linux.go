package procs

import "os"

func treeUsage(pgid int) (Usage, error) {
	rows, err := rowsFromProc(os.DirFS("/"))
	if err != nil {
		return nil, err
	}
	u := Usage{}
	for _, r := range tree(rows, pgid) {
		u[r.pid] = r.cpu
	}
	return u, nil
}
