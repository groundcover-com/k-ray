// Package redact removes secret-shaped values from everything that leaves
// the process: tool results, tool errors and audit lines.
//
// Two passes: a structured pass on objects (key names, name/value pairs,
// command-line flags), and a text pass on every string (value patterns,
// key=value pairs, high-entropy tokens). Each redaction becomes a visible
// marker, [REDACTED:<rule>], and is counted. The engine never prints the
// length, a hash or a prefix of a value.
//
// This is pattern-based. It misses secrets in unusual formats; the README
// says so.
package redact

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Rule names, as they appear in markers and counts.
const (
	RuleKeyName     = "key-name"
	RulePEM         = "pem"
	RuleJWT         = "jwt"
	RuleURLPassword = "url-password"
	RuleBearer      = "bearer"
	RuleAWSKey      = "aws-key"
	RuleToken       = "provider-token"
	RuleEntropy     = "high-entropy"
)

const markPrefix = "[REDACTED:"

// Mark returns the visible marker for a rule.
func Mark(rule string) string { return markPrefix + rule + "]" }

// Counts holds redaction counts by rule.
type Counts map[string]int

// Total is the sum of all counts.
func (c Counts) Total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// String is "rule n, rule n" sorted by rule, or "" when empty.
func (c Counts) String() string {
	keys := make([]string, 0, len(c))
	for k, v := range c {
		if v > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
	}
	return strings.Join(parts, ", ")
}

// Redactor applies the rules and counts what it changed. It is safe for
// concurrent use. Use one Redactor per tool call.
type Redactor struct {
	mu        sync.Mutex
	counts    Counts
	extraKeys []string
}

// New returns a Redactor. extraKeyNames adds sensitive key names (from the
// config file) to the built-in list.
func New(extraKeyNames []string) *Redactor {
	r := &Redactor{counts: Counts{}}
	for _, k := range extraKeyNames {
		if n := normalize(k); n != "" {
			r.extraKeys = append(r.extraKeys, n)
		}
	}
	return r
}

// Counts returns a copy of the counts so far.
func (r *Redactor) Counts() Counts {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := Counts{}
	for k, v := range r.counts {
		c[k] = v
	}
	return c
}

func (r *Redactor) mark(rule string) string {
	r.mu.Lock()
	r.counts[rule]++
	r.mu.Unlock()
	return Mark(rule)
}

// --- sensitive key names ---

// containsWords make a key sensitive when the normalized key contains them.
var containsWords = []string{
	"password", "passwd", "passphrase", "secret", "token", "apikey", "accesskey",
	"privatekey", "privkey", "secretkey", "credential", "clientsecret",
	"connectionstring", "connstring", "connstr", "authorization", "cookie",
	"signingkey", "encryptionkey", "masterkey", "licensekey", "accountkey",
	"sharedaccesskey", "sessionkey", "sessionid", "webhookurl", "dburl", "databaseurl",
}

// suffixWords make a key sensitive when the normalized key is or ends with them.
var suffixWords = []string{"pwd", "pass", "auth", "dsn", "salt", "signature", "apisecret", "key"}

// exactWords make a key sensitive only when the normalized key is exactly
// one of them (they are too short to match inside other words).
var exactWords = map[string]bool{"sig": true, "pw": true, "psk": true, "sas": true, "sastoken": true}

// keySuffixes are the "...key" names that hold key material.
var keySuffixes = []string{
	"apikey", "privatekey", "secretkey", "accesskey", "authkey", "signkey", "encryptkey",
	"cryptokey", "tlskey", "aeskey", "hmackey", "deskey", "rsakey", "jwtkey", "privkey", "masterkey", "signingkey",
}

// keyExceptions are keys that hold references or settings, not secret values.
var keyExceptions = map[string]bool{
	"secretname": true, "secretref": true, "secretkeyref": true, "secretproviderclass": true,
	"imagepullsecrets": true, "serviceaccountname": true, "serviceaccount": true,
	"automountserviceaccounttoken": true, "tokenexpirationseconds": true, "secretnamespace": true,
	"key": true, "publickey": true, "keyid": true, "sshkeyname": true, "cachekey": true,
	"routingkey": true, "partitionkey": true, "sortkey": true, "primarykey": true, "foreignkey": true,
	"hashkey": true, "rangekey": true, "lookupkey": true, "monkey": true, "turkey": true,
	"jobkey": true, "idempotencykey": true, "groupkey": true, "labelkey": true, "topologykey": true,
	"passthrough": true, "bypass": true, "compass": true, "surpass": true,
	"author": true, "authority": true, "oauth2proxy": true,
}

func normalize(k string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(k) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Sensitive reports whether a key name marks its value as secret.
func (r *Redactor) Sensitive(key string) bool {
	// Annotation keys: look at the part after the prefix too.
	if i := strings.LastIndex(key, "/"); i >= 0 && i < len(key)-1 {
		if r.Sensitive(key[i+1:]) {
			return true
		}
	}
	n := normalize(key)
	if n == "" || keyExceptions[n] {
		return false
	}
	if exactWords[n] {
		return true
	}
	for _, w := range containsWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	for _, w := range suffixWords {
		if strings.HasSuffix(n, w) {
			// "key" alone is too broad: require a sensitive word before it.
			if w == "key" {
				if n == "sslkey" {
					return true
				}
				for _, ks := range keySuffixes {
					if strings.HasSuffix(n, ks) {
						return true
					}
				}
				continue
			}
			return true
		}
	}
	for _, w := range r.extraKeys {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// --- text pass ---

var (
	pemRE     = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]{1,64}-----.*?(?:-----END [A-Z0-9 ]{1,64}-----|$)`)
	jwtRE     = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	urlCredRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]{1,20}://)([^\s:@/"'<>]{0,256}):([^\s@/"'<>]{1,256})@`)
	// Authorization-like headers: the rest of the line is the credential.
	authHeaderRE = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization|x-api-key|api-key|x-auth-token|x-access-token)("?[ \t]*[:=][ \t]*)("?)([^\r\n"]+)`)
	authSchemeRE = regexp.MustCompile(`(?i)^(bearer|basic|token|ssws|digest|negotiate|apikey|hmac|aws4-hmac-sha256)[ \t]+`)
	// "--flag value" in a command line held in one string.
	flagSpaceRE = regexp.MustCompile(`(^|[\s"'=])(--?[A-Za-z][A-Za-z0-9_.-]*)([ \t]+)([^\s"'-][^\s"']*)`)
	tokenRE     = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,255}|github_pat_[A-Za-z0-9_]{22,255}|glpat-[A-Za-z0-9_-]{20,}|xox[abposr]-[A-Za-z0-9-]{10,}|(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{16,}|AIza[0-9A-Za-z_-]{35}|npm_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{20,}|dop_v1_[a-f0-9]{64}|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}|hvs\.[A-Za-z0-9_-]{20,})`)
	awsKeyRE    = regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|APKA)[A-Z0-9]{16}\b`)
	bearerRE    = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)([A-Za-z0-9._~+/=-]{8,})`)
	// Name/value pairs written as text (env entries in embedded JSON or YAML).
	jsonPairRE    = regexp.MustCompile(`("name"\s*:\s*")([^"\\]{1,256})("\s*,\s*"value"\s*:\s*")([^"\\]+)`)
	jsonPairRevRE = regexp.MustCompile(`("value"\s*:\s*")([^"\\]+)("\s*,\s*"name"\s*:\s*")([^"\\]{1,256})`)
	yamlPairRE    = regexp.MustCompile(`(\bname:[ \t]*["']?)([A-Za-z0-9_.-]{1,256})(["']?[ \t]*\r?\n[ \t]*(?:-[ \t]+)?value:[ \t]*["']?)([^\s"']+)`)
	// key, separator, then a double-quoted, single-quoted or bare value.
	// Separators never cross a line end, so a YAML key with children does
	// not swallow its first child.
	kvRE = regexp.MustCompile(`([A-Za-z0-9_.-]{1,80})(\\"[ \t]*:[ \t]*\\"|"[ \t]*:[ \t]*|'[ \t]*:[ \t]*|[ \t]*=[ \t]*|:[ \t]+|:)(?:"((?:[^"\\\r\n]|\\.)*)"|'([^'\r\n]*)'|([^\s"',;&<>{}\[\]\\]+))`)
	// A quoted key made of words ("api key": "..."). kvRE reads only the last
	// word of such a key, so this form is read whole.
	kvSpacedRE = regexp.MustCompile(`(["'])([A-Za-z0-9_.-]+(?:[ \t]+[A-Za-z0-9_.-]+)+)(["'][ \t]*:[ \t]*)(?:"((?:[^"\\\r\n]|\\.)*)"|'([^'\r\n]*)'|([^\s"',;&<>{}\[\]\\]+))`)
	// An unquoted key of two words (api key=..., client secret: ...). An
	// unquoted phrase is not widened in general, because ordinary text has
	// "monkey business=1" and "primary key: id". Only this fixed list of
	// names counts; data-model terms (primary, partition, sort, foreign,
	// cache or public key) are not in it.
	kvTwoWordRE = regexp.MustCompile(`(?i)\b((?:api[ \t]+(?:key|token|secret)|access[ \t]+(?:key|token)|secret[ \t]+key|private[ \t]+key|auth[ \t]+(?:key|token)|client[ \t]+(?:secret|key)|(?:refresh|session|bearer)[ \t]+token))([ \t]*=[ \t]*|:[ \t]*)(?:"((?:[^"\\\r\n]|\\.)*)"|'([^'\r\n]*)'|([^\s"',;&<>{}\[\]\\]+))`)
	candRE      = regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`)
)

// Text redacts secret-shaped values in free text.
func (r *Redactor) Text(s string) string {
	if s == "" {
		return s
	}
	s = pemRE.ReplaceAllStringFunc(s, func(string) string { return r.mark(RulePEM) })
	s = jwtRE.ReplaceAllStringFunc(s, func(string) string { return r.mark(RuleJWT) })
	s = urlCredRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := urlCredRE.FindStringSubmatch(m)
		return sub[1] + sub[2] + ":" + r.mark(RuleURLPassword) + "@"
	})
	s = authHeaderRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := authHeaderRE.FindStringSubmatch(m)
		val := sub[4]
		if isMark(strings.TrimSpace(val)) {
			return m
		}
		rule, keep := RuleKeyName, ""
		if sc := authSchemeRE.FindString(val); sc != "" {
			keep = sc
			if strings.HasPrefix(strings.ToLower(sc), "bearer") || strings.HasPrefix(strings.ToLower(sc), "basic") {
				rule = RuleBearer
			}
			if strings.TrimSpace(val[len(sc):]) == "" || isMark(strings.TrimSpace(val[len(sc):])) {
				return m
			}
		}
		return sub[1] + sub[2] + sub[3] + keep + r.mark(rule)
	})
	s = tokenRE.ReplaceAllStringFunc(s, func(string) string { return r.mark(RuleToken) })
	s = awsKeyRE.ReplaceAllStringFunc(s, func(string) string { return r.mark(RuleAWSKey) })
	s = bearerRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := bearerRE.FindStringSubmatch(m)
		return sub[1] + sub[2] + r.mark(RuleBearer)
	})
	pair := func(re *regexp.Regexp, nameIdx, valIdx int) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			sub := re.FindStringSubmatch(m)
			if !r.Sensitive(sub[nameIdx]) || isMark(sub[valIdx]) {
				return m
			}
			sub[valIdx] = r.mark(RuleKeyName)
			return strings.Join(sub[1:], "")
		})
	}
	pair(jsonPairRE, 2, 4)
	pair(jsonPairRevRE, 4, 2)
	pair(yamlPairRE, 2, 4)
	s = flagSpaceRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := flagSpaceRE.FindStringSubmatch(m)
		if !r.Sensitive(strings.TrimLeft(sub[2], "-")) || startsWithMark(sub[4]) {
			return m
		}
		return sub[1] + sub[2] + sub[3] + r.mark(RuleKeyName)
	})
	s = r.kvSpaced(s)
	s = r.kvTwoWord(s)
	s = r.kv(s, 0)
	s = r.entropyPass(s)
	return s
}

// entropyPass redacts long random-looking tokens, but keeps names: DNS
// names, identifiers, paths made of them, and AWS ARNs.
func (r *Redactor) entropyPass(s string) string {
	idx := candRE.FindAllStringIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		tok := s[m[0]:m[1]]
		b.WriteString(s[last:m[0]])
		last = m[1]
		if inARN(s, m[0]) || nameLike(tok) || !highEntropy(tok) {
			b.WriteString(tok)
			continue
		}
		b.WriteString(r.mark(RuleEntropy))
	}
	b.WriteString(s[last:])
	return b.String()
}

// inARN reports whether position i is inside an "arn:" resource name.
func inARN(s string, i int) bool {
	start := i - 160
	if start < 0 {
		start = 0
	}
	j := strings.LastIndex(s[start:i], "arn:")
	if j < 0 {
		return false
	}
	return !strings.ContainsAny(s[start+j:i], " \t\r\n\"'`,;")
}

var (
	lowerWordRE = regexp.MustCompile(`^[a-z]+$`)
	dnsLikeRE   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)
)

// nameLike is true when every "/" segment of tok is a lowercase word, a
// DNS-style name with dashes, or an identifier made of real words
// (PaymentGatewayClientConfiguration2Factory). Random tokens fail it.
func nameLike(tok string) bool {
	segs := strings.Split(strings.TrimRight(tok, "="), "/")
	seen := 0
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		seen++
		switch {
		case lowerWordRE.MatchString(seg):
		case dnsLikeRE.MatchString(seg) && partsAtMost(seg, 24):
		case identifierLike(seg):
		default:
			return false
		}
	}
	return seen > 0
}

func partsAtMost(s string, n int) bool {
	for _, p := range strings.Split(s, "-") {
		if len(p) > n {
			return false
		}
	}
	return true
}

// identifierLike splits on '-', '_' and camel-case boundaries. Every word
// must have at least 3 letters and a vowel, with digits only at its end.
func identifierLike(seg string) bool {
	var words []string
	for _, part := range strings.FieldsFunc(seg, func(c rune) bool { return c == '-' || c == '_' || c == '.' || c == '+' }) {
		start := 0
		for i := 1; i < len(part); i++ {
			prev, c := part[i-1], part[i]
			if c >= 'A' && c <= 'Z' && (prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9') {
				words = append(words, part[start:i])
				start = i
			}
		}
		words = append(words, part[start:])
	}
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		letters, vowel, digitSeen := 0, false, false
		for _, c := range w {
			switch {
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
				if digitSeen {
					return false
				}
				letters++
				if strings.ContainsRune("aeiouyAEIOUY", c) {
					vowel = true
				}
			case c >= '0' && c <= '9':
				digitSeen = true
			default:
				return false
			}
		}
		if letters < 3 || !vowel {
			return false
		}
	}
	return true
}

// kvSpaced redacts the value of a pair whose quoted key has spaces, when the
// key is a sensitive name: spaces count like "_" and "-".
func (r *Redactor) kvSpaced(s string) string {
	return kvSpacedRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := kvSpacedRE.FindStringSubmatch(m)
		if !r.Sensitive(sub[2]) {
			return m
		}
		head := sub[1] + sub[2] + sub[3]
		rest := m[len(head):]
		var val string
		open := ""
		switch {
		case strings.HasPrefix(rest, `"`) && len(rest) >= 2:
			open, val = `"`, sub[4]
		case strings.HasPrefix(rest, "'") && len(rest) >= 2:
			open, val = "'", sub[5]
		default:
			val = sub[6]
		}
		if isMark(val) {
			return m
		}
		return head + open + r.mark(RuleKeyName) + open
	})
}

// kvTwoWord redacts the value after an unquoted two-word sensitive name
// (kvTwoWordRE). The name and the separator stay.
func (r *Redactor) kvTwoWord(s string) string {
	return kvTwoWordRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := kvTwoWordRE.FindStringSubmatch(m)
		head := sub[1] + sub[2]
		rest := m[len(head):]
		val, open := sub[5], ""
		switch {
		case strings.HasPrefix(rest, `"`):
			open, val = `"`, sub[3]
		case strings.HasPrefix(rest, "'"):
			open, val = "'", sub[4]
		}
		if startsWithMark(val) {
			return m
		}
		return head + open + r.mark(RuleKeyName) + open
	})
}

// kv redacts the value of every key=value, key: value and "key":"value" pair
// with a sensitive key. Quoted values are read to the closing quote. A
// non-sensitive value is searched again, so pairs nested in it (a URL query,
// a connection string) are found too.
func (r *Redactor) kv(s string, depth int) string {
	if depth > 8 {
		return s
	}
	return kvRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := kvRE.FindStringSubmatch(m)
		key, sep := sub[1], sub[2]
		open, val := "", sub[5]
		switch {
		case strings.HasPrefix(m[len(key)+len(sep):], `"`):
			open, val = `"`, sub[3]
		case strings.HasPrefix(m[len(key)+len(sep):], `'`):
			open, val = `'`, sub[4]
		}
		if isMark(val) || authSchemeRE.MatchString(val+" ") {
			return m
		}
		if !r.Sensitive(key) {
			return key + sep + open + r.kv(val, depth+1) + open
		}
		return key + sep + open + r.mark(RuleKeyName) + open
	})
}

// highEntropy decides whether a long token looks random enough to be a
// secret. Hex-only tokens (digests, UIDs, container IDs, trace IDs) are
// kept; so are words joined with dashes.
func highEntropy(s string) bool {
	if len(s) < 32 {
		return false
	}
	hexOnly := true
	var lower, upper, digit, b64 bool
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= '0' && c <= '9':
			digit = true
		case c == '+' || c == '/':
			b64 = true
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == '-') {
			hexOnly = false
		}
	}
	if hexOnly {
		return false
	}
	h := entropy(s)
	switch {
	case lower && upper && digit:
		return h >= 4.0
	case b64 && lower && upper:
		return h >= 4.0
	case (lower || upper) && digit && len(s) >= 40:
		return h >= 4.3
	}
	return false
}

func entropy(s string) float64 {
	freq := map[rune]int{}
	n := 0
	for _, c := range s {
		freq[c]++
		n++
	}
	h := 0.0
	for _, f := range freq {
		p := float64(f) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// --- structured pass ---

// Object redacts an unstructured object in place: key names, name/value
// pairs, command-line flags, then the text pass on every string.
func (r *Redactor) Object(obj map[string]any) {
	r.walkMap(obj)
}

// Args redacts the elements of a command line, one element at a time, with
// the rules that Object applies to the "command" and "args" lists of a
// container: the value of a sensitive flag is the whole next element
// ("--password" "correct horse battery"), or the rest of one element
// ("--password=correct horse battery"). Join the result only after this.
func (r *Redactor) Args(args []string) []string {
	list := make([]any, len(args))
	for i, a := range args {
		list[i] = a
	}
	r.walkMap(map[string]any{"args": list})
	out := make([]string, len(args))
	for i, a := range list {
		out[i], _ = a.(string)
	}
	return out
}

// Value redacts any JSON-like value and returns it.
func (r *Redactor) Value(v any) any {
	switch x := v.(type) {
	case map[string]any:
		r.walkMap(x)
		return x
	case []any:
		r.walkSlice(x, "")
		return x
	case string:
		return r.Text(x)
	}
	return v
}

func (r *Redactor) walkMap(m map[string]any) {
	// Name/value pairs: env entries, probe httpHeaders.
	if name, ok := m["name"].(string); ok {
		if v, ok := m["value"].(string); ok && v != "" && !isMark(v) && r.Sensitive(name) {
			m["value"] = r.mark(RuleKeyName)
		}
	}
	for k, v := range m {
		switch x := v.(type) {
		case string:
			if x == "" || isMark(x) {
				continue
			}
			if r.Sensitive(k) {
				m[k] = r.mark(RuleKeyName)
				continue
			}
			if js, ok := r.embeddedJSON(x); ok {
				m[k] = js
				continue
			}
			m[k] = r.Text(x)
		case map[string]any:
			r.walkMap(x)
		case []any:
			if k == "args" || k == "command" {
				r.flags(x)
			}
			if r.Sensitive(k) && allStrings(x) && !keyExceptions[normalize(k)] {
				for i := range x {
					x[i] = r.mark(RuleKeyName)
				}
				continue
			}
			r.walkSlice(x, k)
		}
	}
}

func (r *Redactor) walkSlice(s []any, _ string) {
	for i, v := range s {
		switch x := v.(type) {
		case string:
			if !isMark(x) {
				s[i] = r.Text(x)
			}
		case map[string]any:
			r.walkMap(x)
		case []any:
			r.walkSlice(x, "")
		}
	}
}

// flags redacts --name=value and "--name value" when name is sensitive.
func (r *Redactor) flags(args []any) {
	for i := 0; i < len(args); i++ {
		a, ok := args[i].(string)
		if !ok || !strings.HasPrefix(a, "-") {
			continue
		}
		flag := strings.TrimLeft(a, "-")
		if name, val, ok := strings.Cut(flag, "="); ok {
			if val != "" && r.Sensitive(name) {
				args[i] = a[:len(a)-len(val)] + r.mark(RuleKeyName)
			}
			continue
		}
		if r.Sensitive(flag) && i+1 < len(args) {
			if next, ok := args[i+1].(string); ok && next != "" && !strings.HasPrefix(next, "-") && !isMark(next) {
				args[i+1] = r.mark(RuleKeyName)
				i++
			}
		}
	}
}

// embeddedJSON redacts a string that holds a JSON object or array (for
// example an annotation) with the structured pass, so that name/value pairs
// and sensitive keys inside it are found. The result is compact JSON.
func (r *Redactor) embeddedJSON(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if len(t) < 2 || !(t[0] == '{' && t[len(t)-1] == '}' || t[0] == '[' && t[len(t)-1] == ']') {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return "", false
	}
	v = r.Value(v)
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func allStrings(s []any) bool {
	if len(s) == 0 {
		return false
	}
	for _, v := range s {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

func isMark(s string) bool { return strings.HasPrefix(s, markPrefix) && strings.HasSuffix(s, "]") }

// startsWithMark reports whether s begins with a whole mark; punctuation may
// follow it.
func startsWithMark(s string) bool {
	return strings.HasPrefix(s, markPrefix) && strings.IndexByte(s, ']') > 0
}
