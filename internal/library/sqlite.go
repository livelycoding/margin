package library

import "net/url"

func sqliteDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_busy_timeout", "5000")
	q.Set("_foreign_keys", "on")
	u.RawQuery = q.Encode()
	return u.String()
}
