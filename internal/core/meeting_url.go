package core

import (
	"net/url"
	"regexp"
	"strings"
)

// Video-call providers MeetingProvider recognises.
const (
	ProviderMeet  = "Meet"
	ProviderZoom  = "Zoom"
	ProviderTeams = "Teams"
	ProviderWebex = "Webex"
	ProviderJitsi = "Jitsi"
)

var meetCodeRe = regexp.MustCompile(`^/[a-z]{3}-[a-z]{4}-[a-z]{3}(/|$)`)

// MeetingProvider names the video-call service rawURL joins ("" when it
// is not a meeting link). It matches on the host and the path shape, not
// on a substring, so a page that merely mentions a provider (a Teams
// "meetingOptions" settings link, a Zoom marketing page) is not a join
// link. The URL must be http(s).
func MeetingProvider(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()
	hostIs := func(suffix string) bool { return host == suffix || strings.HasSuffix(host, "."+suffix) }
	switch {
	case host == "meet.google.com":
		if meetCodeRe.MatchString(path) || strings.HasPrefix(path, "/lookup/") {
			return ProviderMeet
		}
	case hostIs("zoom.us") || hostIs("zoomgov.com"):
		for _, p := range []string{"/j/", "/my/", "/wc/join/", "/s/"} {
			if strings.HasPrefix(path, p) {
				return ProviderZoom
			}
		}
	case host == "teams.microsoft.com":
		if strings.HasPrefix(path, "/l/meetup-join/") || strings.HasPrefix(path, "/meet/") {
			return ProviderTeams
		}
	case host == "teams.live.com":
		if strings.HasPrefix(path, "/meet/") {
			return ProviderTeams
		}
	case hostIs("webex.com"):
		for _, p := range []string{"/meet/", "/join/", "/j.php", "/wbxmjs/"} {
			if strings.Contains(path, p) {
				return ProviderWebex
			}
		}
	case host == "meet.jit.si" || host == "jit.si" || host == "8x8.vc":
		if len(strings.Trim(path, "/")) > 0 {
			return ProviderJitsi
		}
	}
	return ""
}

var urlInTextRe = regexp.MustCompile("https?://[^\\s<>\"'`\\\\)\\]]+")

// FindMeetingURL returns the first meeting link in the texts, searched in
// order. Links wrapped by a mail security scanner (Outlook Safe Links,
// Google's redirector) are unwrapped first; trailing sentence punctuation
// is dropped.
func FindMeetingURL(texts ...string) string {
	for _, text := range texts {
		for _, raw := range urlInTextRe.FindAllString(text, -1) {
			raw = strings.TrimRight(raw, ".,;:!?")
			if u := unwrapRedirect(raw); MeetingProvider(u) != "" {
				return u
			}
		}
	}
	return ""
}

// unwrapRedirect returns the target of a link-scanner wrapper, or raw.
func unwrapRedirect(raw string) string {
	for range 3 {
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		host := strings.ToLower(u.Hostname())
		var target string
		switch {
		case strings.HasSuffix(host, ".safelinks.protection.outlook.com"):
			target = u.Query().Get("url")
		case (host == "www.google.com" || host == "google.com") && u.Path == "/url":
			target = u.Query().Get("q")
			if target == "" {
				target = u.Query().Get("url")
			}
		}
		if target == "" {
			return raw
		}
		raw = target
	}
	return raw
}
