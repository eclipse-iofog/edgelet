package modelpull

import "io"

// WatchReader wraps r and reports cumulative downloaded bytes as data is copied.
func WatchReader(r io.Reader, already, total int64, report func(downloaded, total int64)) io.Reader {
	if r == nil || report == nil {
		return r
	}
	return &watchReader{r: r, got: already, total: total, report: report}
}

type watchReader struct {
	r      io.Reader
	got    int64
	total  int64
	report func(downloaded, total int64)
}

func (w *watchReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.got += int64(n)
		w.report(w.got, w.total)
	}
	return n, err
}

// ReportProgress invokes OnProgress when a callback is set.
func (r Request) ReportProgress(downloaded, total int64) {
	if r.OnProgress != nil {
		r.OnProgress(downloaded, total)
	}
}
