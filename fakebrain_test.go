package main

import (
	"context"
	"regexp"
	"strings"

	llmkit "github.com/muthuishere/toolnexus/golang"
)

// Test models. A canned answer is written as call:name{key:<escape>value<escape>,...}; several
// calls in one answer are separated by " | ". The fake brain runs them through the real toolkit,
// the way the model client does after a chat completion.

type fakeModel map[string]string

func (f fakeModel) Run(ctx context.Context, text string, tk *llmkit.Toolkit) (llmkit.RunResult, error) {
	return runCanned(ctx, f[text], tk)
}

// stub answers every message with the same canned output.
type stub string

func (s stub) Run(ctx context.Context, _ string, tk *llmkit.Toolkit) (llmkit.RunResult, error) {
	return runCanned(ctx, string(s), tk)
}

func runCanned(ctx context.Context, raw string, tk *llmkit.Toolkit) (llmkit.RunResult, error) {
	var res llmkit.RunResult
	for _, part := range strings.Split(raw, " | ") {
		c, ok := parseCanned(part)
		if !ok {
			res.Text += part
			continue
		}
		args := map[string]any{}
		for k, v := range c.Args {
			args[k] = v
		}
		tc := llmkit.ToolCall{Name: c.Name, Args: args}
		if _, found := tk.Get(c.Name); found {
			r, err := tk.Execute(ctx, c.Name, args)
			if err != nil {
				tc.IsError = true
			}
			_ = r
		} else {
			tc.IsError = true
		}
		res.ToolCalls = append(res.ToolCalls, tc)
	}
	return res, nil
}

var callRe = regexp.MustCompile(`call:([A-Za-z_][A-Za-z0-9_]*)(.*)`)

const esc = "<escape>"

func parseCanned(raw string) (Call, bool) {
	m := callRe.FindStringSubmatch(raw)
	if m == nil {
		return Call{}, false
	}
	c := Call{Name: m[1], Args: map[string]string{}}
	s := strings.TrimPrefix(m[2], "{")
	for {
		s = strings.TrimLeft(s, " ,")
		if s == "" || s[0] == '}' {
			break
		}
		colon := strings.IndexByte(s, ':')
		if colon < 0 {
			break
		}
		key := strings.TrimSpace(s[:colon])
		s = s[colon+1:]
		var val string
		if strings.HasPrefix(s, esc) {
			s = s[len(esc):]
			end := strings.Index(s, esc)
			if end < 0 {
				val, s = s, ""
			} else {
				val, s = s[:end], s[end+len(esc):]
			}
		} else {
			end := strings.IndexAny(s, ",}")
			if end < 0 {
				val, s = s, ""
			} else {
				val, s = s[:end], s[end:]
			}
		}
		if key == "" || strings.Contains(key, esc) {
			break
		}
		c.Args[key] = strings.TrimSpace(val)
	}
	return c, true
}
