package scenarios

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunAssertions hits baseURL with each assertion. Auth, when requested,
// is Basic you@example.com:issuetap (the default local credential).
func RunAssertions(baseURL string, sc Scenario) Report {
	rep := Report{Name: sc.Name, Passed: true}
	client := &http.Client{Timeout: 15 * time.Second}
	baseURL = strings.TrimRight(baseURL, "/")
	for _, a := range sc.Assertions {
		res := runOne(client, baseURL, a)
		if !res.Passed {
			rep.Passed = false
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

func runOne(client *http.Client, base string, a Assertion) AssertionResult {
	name := a.Name
	if name == "" {
		name = a.Method + " " + a.Path
	}
	method := a.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if a.Body != nil {
		b, err := json.Marshal(a.Body)
		if err != nil {
			return AssertionResult{Name: name, Passed: false, Detail: err.Error()}
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+a.Path, body)
	if err != nil {
		return AssertionResult{Name: name, Passed: false, Detail: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	if a.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	if a.Auth {
		tok := base64.StdEncoding.EncodeToString([]byte("you@example.com:issuetap"))
		req.Header.Set("Authorization", "Basic "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return AssertionResult{Name: name, Passed: false, Detail: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	want := a.WantStatus
	if want == 0 {
		want = http.StatusOK
	}
	if resp.StatusCode != want {
		return AssertionResult{
			Name: name, Passed: false,
			Got: fmt.Sprintf("status %d", resp.StatusCode),
			Want: fmt.Sprintf("status %d", want),
			Detail: clip(string(raw), 300),
		}
	}
	if a.WantContains != "" && !strings.Contains(string(raw), a.WantContains) {
		return AssertionResult{
			Name: name, Passed: false,
			Got: clip(string(raw), 300), Want: "contains " + a.WantContains,
		}
	}
	if a.WantJSONPath != "" {
		got, ok := jsonPath(raw, a.WantJSONPath)
		if !ok || (a.WantJSONValue != "" && got != a.WantJSONValue) {
			return AssertionResult{
				Name: name, Passed: false,
				Got: got, Want: a.WantJSONPath + "=" + a.WantJSONValue,
			}
		}
	}
	return AssertionResult{Name: name, Passed: true, Got: fmt.Sprintf("status %d", resp.StatusCode)}
}

func jsonPath(raw []byte, path string) (string, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	cur := v
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[p]
		if !ok {
			return "", false
		}
	}
	switch t := cur.(type) {
	case string:
		return t, true
	case float64:
		return fmt.Sprintf("%v", t), true
	case bool:
		return fmt.Sprintf("%v", t), true
	default:
		b, _ := json.Marshal(t)
		return string(b), true
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
