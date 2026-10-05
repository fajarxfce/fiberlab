package cwmp

import (
	"crypto/md5" // TR-069 HTTP Digest interoperability, not password storage.
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func randomToken() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

// parseAuth accepts quoted commas and escaped characters in a Digest challenge.
func parseAuth(value string) (string, map[string]string) {
	scheme, tail, _ := strings.Cut(strings.TrimSpace(value), " ")
	p := map[string]string{}
	for len(tail) > 0 {
		tail = strings.TrimLeft(tail, " ,\t")
		key, rest, ok := strings.Cut(tail, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		rest = strings.TrimLeft(rest, " \t")
		var b strings.Builder
		if strings.HasPrefix(rest, `"`) {
			rest = rest[1:]
			for len(rest) > 0 {
				ch := rest[0]
				rest = rest[1:]
				if ch == '"' {
					break
				}
				if ch == '\\' && len(rest) > 0 {
					ch = rest[0]
					rest = rest[1:]
				}
				b.WriteByte(ch)
			}
		} else {
			var item string
			item, rest, _ = strings.Cut(rest, ",")
			b.WriteString(strings.TrimSpace(item))
		}
		p[key] = b.String()
		tail = rest
	}
	return strings.ToLower(scheme), p
}

func digestHash(algorithm, s string) string {
	if strings.HasPrefix(strings.ToUpper(algorithm), "SHA-256") {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func digestResponse(p map[string]string, username, password, method string) string {
	algorithm := strings.ToUpper(p["algorithm"])
	a1 := digestHash(algorithm, username+":"+p["realm"]+":"+password)
	if strings.HasSuffix(algorithm, "-SESS") {
		a1 = digestHash(algorithm, a1+":"+p["nonce"]+":"+p["cnonce"])
	}
	a2 := digestHash(algorithm, method+":"+p["uri"])
	if p["qop"] == "auth" {
		return digestHash(algorithm, a1+":"+p["nonce"]+":"+p["nc"]+":"+p["cnonce"]+":auth:"+a2)
	}
	return digestHash(algorithm, a1+":"+p["nonce"]+":"+a2)
}

type httpAuth struct {
	Scheme string
	Params map[string]string
	Count  uint32
}

func (a *httpAuth) challenge(headers http.Header) error {
	for _, challenge := range headers.Values("WWW-Authenticate") {
		scheme, p := parseAuth(challenge)
		if scheme == "digest" {
			algorithm := strings.ToUpper(p["algorithm"])
			if algorithm != "" && algorithm != "MD5" && algorithm != "MD5-SESS" && algorithm != "SHA-256" && algorithm != "SHA-256-SESS" {
				continue
			}
			if p["nonce"] == "" {
				continue
			}
			if p["qop"] != "" {
				found := false
				for _, q := range strings.Split(p["qop"], ",") {
					if strings.TrimSpace(q) == "auth" {
						found = true
					}
				}
				if !found {
					continue
				}
				p["qop"] = "auth"
			}
			a.Scheme = scheme
			a.Params = p
			a.Count = 0
			return nil
		}
	}
	for _, challenge := range headers.Values("WWW-Authenticate") {
		if scheme, _ := parseAuth(challenge); scheme == "basic" {
			a.Scheme = scheme
			return nil
		}
	}
	return fmt.Errorf("ACS requires an unsupported HTTP authentication scheme")
}

func (a *httpAuth) apply(r *http.Request, username, password string) {
	if a.Scheme == "basic" {
		r.SetBasicAuth(username, password)
		return
	}
	if a.Scheme != "digest" {
		return
	}
	a.Count++
	p := map[string]string{}
	for k, v := range a.Params {
		p[k] = v
	}
	p["uri"] = r.URL.RequestURI()
	p["cnonce"] = randomToken()
	p["nc"] = fmt.Sprintf("%08x", a.Count)
	fields := []string{`username=` + strconv.Quote(username), `realm=` + strconv.Quote(p["realm"]), `nonce=` + strconv.Quote(p["nonce"]), `uri=` + strconv.Quote(p["uri"]), `response=` + strconv.Quote(digestResponse(p, username, password, r.Method))}
	if p["algorithm"] != "" {
		fields = append(fields, "algorithm="+p["algorithm"])
	}
	if p["opaque"] != "" {
		fields = append(fields, "opaque="+strconv.Quote(p["opaque"]))
	}
	if p["qop"] != "" {
		fields = append(fields, "qop=auth", "nc="+p["nc"], "cnonce="+strconv.Quote(p["cnonce"]))
	} else if strings.HasSuffix(strings.ToUpper(p["algorithm"]), "-SESS") {
		fields = append(fields, "cnonce="+strconv.Quote(p["cnonce"]))
	}
	r.Header.Set("Authorization", "Digest "+strings.Join(fields, ", "))
}

type connectionAuth struct {
	Nonce string
	At    time.Time
	Count uint64
}

func (a *connectionAuth) check(r *http.Request, username, password string) bool {
	scheme, p := parseAuth(r.Header.Get("Authorization"))
	if scheme != "digest" || time.Since(a.At) > 5*time.Minute || a.Nonce == "" || p["nonce"] != a.Nonce || p["realm"] != "Fiberlab" || p["username"] != username || p["uri"] != r.URL.RequestURI() || p["qop"] != "auth" || p["cnonce"] == "" {
		return false
	}
	if p["algorithm"] != "" && !strings.EqualFold(p["algorithm"], "MD5") {
		return false
	}
	nc, err := strconv.ParseUint(p["nc"], 16, 32)
	if err != nil || len(p["nc"]) != 8 || nc <= a.Count {
		return false
	}
	want := digestResponse(p, username, password, r.Method)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(p["response"])), []byte(want)) != 1 {
		return false
	}
	a.Count = nc
	return true
}

func (a *connectionAuth) challenge(w http.ResponseWriter) {
	// Each new challenge starts an independent Digest exchange. GenieACS opens
	// a new HTTP client for each task and starts its nonce count at one again.
	// Reusing the old challenge with a global count would reject the second task.
	a.Nonce = randomToken()
	a.At = time.Now()
	a.Count = 0
	w.Header().Set("WWW-Authenticate", `Digest realm="Fiberlab", nonce="`+a.Nonce+`", algorithm=MD5, qop="auth"`)
	w.WriteHeader(http.StatusUnauthorized)
}
