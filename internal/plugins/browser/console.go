package browser

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// consoleLine is the line of the console of a page the event method with
// params says, like "[error] not found", or "" for an event that is none.
func consoleLine(method string, params json.RawMessage) string {
	var line string
	switch method {
	case "Runtime.consoleAPICalled":
		var p struct {
			Type string         `json:"type"`
			Args []remoteObject `json:"args"`
		}
		json.Unmarshal(params, &p)
		parts := make([]string, len(p.Args))
		for i, a := range p.Args {
			parts[i] = a.String()
		}
		line = "[" + p.Type + "] " + strings.Join(parts, " ")
	case "Runtime.exceptionThrown":
		var p struct {
			Details struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		json.Unmarshal(params, &p)
		// The message and where it was thrown, of the whole stack.
		lines := strings.Split(firstOf(p.Details.Exception.Description, p.Details.Text), "\n")
		for i := range lines {
			lines[i] = strings.TrimSpace(lines[i])
		}
		line = "[exception] " + strings.Join(lines[:min(len(lines), 2)], " ")
	case "Log.entryAdded":
		var p struct {
			Entry struct {
				Level string `json:"level"`
				Text  string `json:"text"`
				URL   string `json:"url"`
			} `json:"entry"`
		}
		json.Unmarshal(params, &p)
		if p.Entry.Level == "verbose" {
			return ""
		}
		line = "[" + p.Entry.Level + "] " + p.Entry.Text
		if p.Entry.URL != "" {
			line += " (" + p.Entry.URL + ")"
		}
	default:
		return ""
	}
	if len(line) > maxConsoleLine {
		end := maxConsoleLine
		for !utf8.RuneStart(line[end]) {
			end--
		}
		line = line[:end] + "…"
	}
	return line
}

// remoteObject is a value of the page as DevTools hands it over.
type remoteObject struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description"`
	Preview     *struct {
		Subtype    string `json:"subtype"`
		Overflow   bool   `json:"overflow"`
		Properties []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"properties"`
	} `json:"preview"`
}

// String is the value o as the console of a browser shows it, in short.
func (o remoteObject) String() string {
	switch {
	case o.Type == "string":
		var s string
		json.Unmarshal(o.Value, &s)
		return s
	case o.Type == "undefined":
		return "undefined"
	case o.Subtype == "null":
		return "null"
	case o.Preview != nil:
		parts := make([]string, len(o.Preview.Properties))
		for i, p := range o.Preview.Properties {
			value := p.Value
			if p.Type == "string" {
				value = strconv.Quote(value)
			}
			parts[i] = value
			if o.Preview.Subtype != "array" {
				parts[i] = p.Name + ": " + value
			}
		}
		if o.Preview.Overflow {
			parts = append(parts, "…")
		}
		if o.Preview.Subtype == "array" {
			return "[" + strings.Join(parts, ", ") + "]"
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case o.Value != nil:
		return string(o.Value)
	}
	return firstOf(o.Description, o.Type)
}
