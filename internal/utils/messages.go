package utils

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/AshokShau/gotdbot"
	gh "github.com/google/go-github/v92/github"
	"go.mongodb.org/mongo-driver/v2/mongo"
	xhtml "golang.org/x/net/html"
)

const MessageLimit = 4096

func Clip(s string, limit int) string {
	if limit <= 0 {
		return ""
	}

	s = strings.ToValidUTF8(s, "�")
	units := 0
	for _, r := range s {
		units++
		if r > 0xffff {
			units++
		}
	}
	if units <= limit {
		return s
	}
	units = 0
	for i, r := range s {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > limit-1 {
			return s[:i] + "…"
		}
		units += n
	}
	return s
}

func Code(s string) string {
	if s == "" {
		return ""
	}
	return "<code>" + html.EscapeString(s) + "</code>"
}

func SafeURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.String()
}

func Link(label, target string) string {
	if label == "" {
		return ""
	}
	if target = SafeURL(target); target != "" {
		return `<a href="` + html.EscapeString(target) + `">` + html.EscapeString(label) + `</a>`
	}
	return html.EscapeString(label)
}

func LimitHTML(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	z := xhtml.NewTokenizer(strings.NewReader(s))
	var out strings.Builder
	var stack []string
	remaining := limit - 1
	truncated := false
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			if z.Err() != io.EOF {
				truncated = true
			}
			break
		}
		t := z.Token()
		switch tt {
		case xhtml.TextToken:
			clipped := Clip(t.Data, remaining)
			if clipped != t.Data {
				truncated = true
			}
			if truncated {
				clipped = strings.TrimSuffix(clipped, "…")
			}
			out.WriteString(html.EscapeString(clipped))
			for _, r := range clipped {
				remaining--
				if r > 0xffff {
					remaining--
				}
			}
		case xhtml.StartTagToken:
			switch t.Data {
			case "b", "i", "u", "s", "code", "pre", "blockquote":
				out.WriteString("<" + t.Data + ">")
				stack = append(stack, t.Data)
			case "a":
				for _, a := range t.Attr {
					if a.Key == "href" && SafeURL(a.Val) != "" {
						out.WriteString(`<a href="` + html.EscapeString(a.Val) + `">`)
						stack = append(stack, "a")
						break
					}
				}
			}
		case xhtml.EndTagToken:
			if len(stack) > 0 && stack[len(stack)-1] == t.Data {
				out.WriteString("</" + t.Data + ">")
				stack = stack[:len(stack)-1]
			}
		}
		if truncated {
			break
		}
	}
	for _, s := range slices.Backward(stack) {
		out.WriteString("</" + s + ">")
	}
	if truncated {
		out.WriteString("…")
	}
	return out.String()
}

func OperationError(operation string, err error) string {
	next := "Please try again. If the problem continues, contact the bot administrator."
	var rate *gh.RateLimitError
	var abuse *gh.AbuseRateLimitError
	var api *gh.ErrorResponse
	switch {
	case errors.As(err, &rate), errors.As(err, &abuse):
		next = "GitHub's rate limit was reached. Wait before trying again."
	case errors.As(err, &api) && api.Response != nil:
		switch api.Response.StatusCode {
		case 401:
			next = "GitHub authorization has expired. Use /connect to reconnect."
		case 403:
			next = "Check your GitHub permissions. If GitHub is limiting requests, wait before trying again."
		case 404:
			next = "Check that the resource exists and your GitHub account has access."
		case 409, 422:
			next = "Check the arguments and the resource's current state on GitHub, then try again."
		default:
			next = "GitHub is unavailable. Please try again later."
		}
	}
	return strings.TrimRight(operation, ".: ") + ".\n\n" + next
}

func ErrorDetails(err error) string {
	if telegramError, ok := errors.AsType[*gotdbot.Error](err); ok {
		details := fmt.Sprintf("error_type=%T telegram_code=%d", err, telegramError.Code)
		if telegramError.Code == 406 {
			return details
		}
		if retryAfter := telegramError.GetRetryAfter(); retryAfter > 0 {
			details += fmt.Sprintf(" retry_after_seconds=%d", retryAfter)
		}
		details += fmt.Sprintf(" telegram_message=%q", telegramError.Message)
		return details
	}
	if errors.Is(err, gotdbot.SendTimeout) {
		return fmt.Sprintf("error_type=%T telegram_timeout=true", err)
	}
	var api *gh.ErrorResponse
	if errors.As(err, &api) && api.Response != nil && api.Response.StatusCode != 0 {
		return fmt.Sprintf("error_type=%T http_status=%d", err, api.Response.StatusCode)
	}
	return fmt.Sprintf("error_type=%T", err)
}

func ShortSHA(s string) string {
	if utf8.RuneCountInString(s) > 7 {
		return string([]rune(s)[:7])
	}
	return s
}

func ConnectionError(err error) string {
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return OperationError("Could not load your GitHub connection", err)
	}
	return "GitHub is not connected. Use /connect in a private chat, then try again."
}
