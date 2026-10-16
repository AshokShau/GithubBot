package utils

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

const telegramRichHTMLTags = " a b strong i em u ins s strike del code mark sub sup tg-spoiler tg-reference tg-emoji tg-time tg-math h1 h2 h3 h4 h5 h6 p br pre footer hr ul ol li input blockquote cite aside img video audio tg-document figure figcaption tg-map tg-collage tg-slideshow table caption tr th td details summary tg-math-block tg-button tg-button-row "

var (
	githubReference       = regexp.MustCompile(`^(?:(?:[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+)?#[1-9][0-9]*|GH-[1-9][0-9]*|(?:[A-Za-z0-9][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_.-]+)?@)?[a-fA-F0-9]{40}|@[A-Za-z0-9][A-Za-z0-9-]*(?:/[A-Za-z0-9_.-]+)?)`)
	markdownURL           = regexp.MustCompile(`(?i)^(?:https?://|mailto:)[^\s<>"]+`)
	markdownFence         = regexp.MustCompile("^(?:[ \\t]*>[ \\t]?)*[ \\t]*(?:[-+*] |[0-9]+[.)] )?(`{3,}|~{3,})([^\\n]*)")
	markdownHeadingPrefix = regexp.MustCompile(`^(?:[ \t]*>[ \t]?)*[ \t]*(?:[-+*] |[0-9]+[.)] )?$`)
	markdownQuotedCode    = regexp.MustCompile(`^(?: {0,3}> ?)+(?: {4}|\t)`)
	markdownDefinition    = regexp.MustCompile(`^ {0,3}\[[^\]\n]+\]:`)
	htmlNumericEntity     = regexp.MustCompile(`^&#(?:[0-9]+|[xX][0-9a-fA-F]+);`)
	htmlNamedEntity       = regexp.MustCompile(`&[A-Za-z][A-Za-z0-9]+;`)
)

func GitHubMarkdown(source, repositoryURL string) string {
	repositoryURL = strings.TrimRight(SafeURL(repositoryURL), "/")
	var host, repository string
	if parsed, err := url.Parse(repositoryURL); err == nil && parsed.Host != "" {
		host = parsed.Scheme + "://" + parsed.Host
		repository = strings.Trim(parsed.Path, "/")
	}
	var out strings.Builder
	var fence string
	var fencePrefix string
	anchorDepth := 0
	var anchors []bool
	for i := 0; i < len(source); {
		if i == 0 || source[i-1] == '\n' {
			end := i + strings.IndexByte(source[i:], '\n')
			if end < i {
				end = len(source)
			} else {
				end++
			}
			line := source[i:end]
			marker := markdownFence.FindStringSubmatch(line)
			if fence != "" {
				out.WriteString(line)
				i = end
				if len(marker) > 0 && marker[1][0] == fence[0] && len(marker[1]) >= len(fence) && strings.TrimSpace(marker[2]) == "" {
					fence = ""
				}
				continue
			}
			if len(marker) > 0 {
				fence = marker[1]
				fencePrefix = strings.Map(func(r rune) rune {
					if r == '>' || unicode.IsSpace(r) {
						return r
					}
					return ' '
				}, line[:strings.Index(line, fence)])
				out.WriteString(line)
				i = end
				continue
			}
			if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") || markdownQuotedCode.MatchString(line) || markdownDefinition.MatchString(line) {
				out.WriteString(line)
				i = end
				continue
			}
		}
		if source[i] == '\\' && i+1 < len(source) {
			out.WriteString(source[i : i+2])
			i += 2
			continue
		}
		if source[i] == '`' {
			if end := markdownCodeEnd(source, i); end > i {
				out.WriteString(source[i:end])
				i = end
				continue
			}
		}
		if source[i] == '[' || source[i] == '!' && i+1 < len(source) && source[i+1] == '[' {
			start := i
			if source[i] == '!' {
				start++
			}
			if labelEnd, end, empty := markdownLinkEnd(source, start); end > i {
				if empty {
					out.WriteString(GitHubMarkdown(source[start+1:labelEnd], repositoryURL))
				} else {
					out.WriteString(source[i:end])
				}
				i = end
				continue
			}
		}
		if target := markdownURL.FindString(source[i:]); target != "" {
			out.WriteString(target)
			i += len(target)
			continue
		}
		if source[i] == '<' && i+1 < len(source) && (source[i+1] >= 'a' && source[i+1] <= 'z' || source[i+1] >= 'A' && source[i+1] <= 'Z' || source[i+1] == '/' || source[i+1] == '!') {
			// Markdown URL and e-mail autolinks aren't HTML tags.
			if end := strings.IndexByte(source[i:], '>'); end > 0 {
				inner := source[i+1 : i+end]
				if !strings.ContainsAny(inner, " \t\r\n\"'") && (strings.HasPrefix(inner, "https://") || strings.HasPrefix(inner, "http://") || strings.Contains(inner, "@")) {
					out.WriteString(source[i : i+end+1])
					i += end + 1
					continue
				}
			}
			z := xhtml.NewTokenizer(strings.NewReader(source[i:]))
			typeOf := z.Next()
			if typeOf == xhtml.StartTagToken || typeOf == xhtml.EndTagToken || typeOf == xhtml.SelfClosingTagToken {
				raw := string(z.Raw())
				token := z.Token()
				tag := token.Data
				if !strings.Contains(telegramRichHTMLTags, " "+tag+" ") {
					out.WriteString(Code(raw))
				} else {
					if (tag == "pre" || tag == "code") && typeOf == xhtml.StartTagToken {
						end := i + len(raw)
						open := []string{tag}
						for {
							kind := z.Next()
							end += len(z.Raw())
							if kind == xhtml.ErrorToken {
								end = len(source)
								break
							}
							name := z.Token().Data
							if kind == xhtml.StartTagToken && (name == "pre" || name == "code") {
								open = append(open, name)
							} else if kind == xhtml.EndTagToken && name == open[len(open)-1] {
								open = open[:len(open)-1]
								if len(open) == 0 {
									break
								}
							}
						}
						out.WriteString(htmlNamedEntity.ReplaceAllStringFunc(source[i:end], func(entity string) string {
							return html.EscapeString(html.UnescapeString(entity))
						}))
						for index := len(open) - 1; index >= 0; index-- {
							out.WriteString("</" + open[index] + ">")
						}
						i = end
						continue
					}
					keep := true
					if tag == "a" {
						if typeOf == xhtml.StartTagToken {
							for _, attribute := range token.Attr {
								if attribute.Key == "href" && strings.TrimSpace(attribute.Val) == "" {
									keep = false
								}
							}
							anchors = append(anchors, keep)
							if keep {
								anchorDepth++
							}
						} else if typeOf == xhtml.EndTagToken && len(anchors) > 0 {
							keep = anchors[len(anchors)-1]
							anchors = anchors[:len(anchors)-1]
							if keep {
								anchorDepth--
							}
						}
					}
					if keep {
						out.WriteString(htmlNamedEntity.ReplaceAllStringFunc(raw, func(entity string) string {
							return html.EscapeString(html.UnescapeString(entity))
						}))
					}
				}
				i += len(raw)
				continue
			}
			if typeOf == xhtml.CommentToken {
				out.Write(z.Raw())
				i += len(z.Raw())
				continue
			}
		}
		if source[i] == '&' {
			if entity := htmlNumericEntity.FindString(source[i:]); entity != "" {
				out.WriteString(entity)
				i += len(entity)
				continue
			}
			if i+1 < len(source) && (source[i+1] >= 'a' && source[i+1] <= 'z' || source[i+1] >= 'A' && source[i+1] <= 'Z') {
				entity := htmlNamedEntity.FindStringIndex(source[i:])
				if entity != nil && entity[0] == 0 {
					out.WriteString(html.EscapeString(html.UnescapeString(source[i : i+entity[1]])))
					i += entity[1]
					continue
				}
			}
		}
		previous, _ := utf8.DecodeLastRuneInString(source[:i])
		if anchorDepth == 0 && (i == 0 || !unicode.IsLetter(previous) && !unicode.IsDigit(previous) && !strings.ContainsRune("_/#@&", previous)) {
			if reference := githubReference.FindString(source[i:]); reference != "" {
				next, _ := utf8.DecodeRuneInString(source[i+len(reference):])
				if !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '_' {
					target := ""
					switch {
					case strings.HasPrefix(reference, "@") && host != "":
						user := strings.TrimPrefix(reference, "@")
						if organization, team, ok := strings.Cut(user, "/"); ok {
							target = host + "/orgs/" + organization + "/teams/" + team
						} else {
							target = host + "/" + user
						}
					case strings.Contains(reference, "#"):
						repo, number, _ := strings.Cut(reference, "#")
						if repo == "" {
							repo = repository
						}
						if host != "" && repo != "" {
							target = host + "/" + repo + "/issues/" + number
						}
					case strings.HasPrefix(reference, "GH-") && repositoryURL != "":
						target = repositoryURL + "/issues/" + strings.TrimPrefix(reference, "GH-")
					case host != "" && repository != "":
						repo, sha, qualified := strings.Cut(reference, "@")
						if !qualified {
							repo, sha = repository, reference
						} else if !strings.Contains(repo, "/") {
							_, name, _ := strings.Cut(repository, "/")
							repo += "/" + name
						}
						target = host + "/" + repo + "/commit/" + sha
					}
					if target != "" {
						out.WriteString(Link(reference, target))
					} else if strings.HasPrefix(reference, "#") {
						out.WriteString(Code(reference))
					} else {
						out.WriteString(reference)
					}
					i += len(reference)
					continue
				}
			}
		}
		// GFM requires whitespace after an ATX heading marker. Telegram's
		// parser also recognizes #word; escape that form to retain plain text.
		if source[i] == '#' && (i == 0 || markdownHeadingPrefix.MatchString(source[strings.LastIndexByte(source[:i], '\n')+1:i])) {
			end := i
			for end < len(source) && source[end] == '#' {
				end++
			}
			if end < len(source) && source[end] != ' ' && source[end] != '\t' && source[end] != '\n' && source[end] != '\r' {
				out.WriteByte('\\')
			}
		}
		out.WriteByte(source[i])
		i++
	}
	if fence != "" {
		if !strings.HasSuffix(source, "\n") {
			out.WriteByte('\n')
		}
		out.WriteString(fencePrefix + fence + "\n")
	}
	return out.String()
}

func markdownCodeEnd(source string, start int) int {
	end := start
	for end < len(source) && source[end] == '`' {
		end++
	}
	marker := source[start:end]
	for next := end; next < len(source); {
		index := strings.Index(source[next:], marker)
		if index < 0 {
			break
		}
		index += next
		end := index + len(marker)
		for end < len(source) && source[end] == '`' {
			end++
		}
		if end-index == len(marker) {
			return end
		}
		next = end
	}
	return start
}

func markdownLinkEnd(source string, start int) (labelEnd, end int, empty bool) {
	depth := 1
	for i := start + 1; i < len(source); i++ {
		switch source[i] {
		case '\\':
			i++
		case '`':
			if end := markdownCodeEnd(source, i); end > i {
				i = end - 1
			}
		case '[':
			depth++
		case ']':
			depth--
			if depth != 0 {
				continue
			}
			labelEnd = i
			if i+1 < len(source) && source[i+1] == '[' {
				if n := strings.IndexByte(source[i+2:], ']'); n >= 0 {
					return labelEnd, i + 2 + n + 1, false
				}
			}
			if i+1 >= len(source) || source[i+1] != '(' {
				return 0, 0, false
			}
			depth = 1
			for j := i + 2; j < len(source); j++ {
				switch source[j] {
				case '\\':
					j++
				case '<':
					if end := strings.IndexByte(source[j+1:], '>'); end >= 0 {
						j += end + 1
					}
				case '\'', '"':
					if j == i+2 || strings.ContainsRune(" \t\r\n", rune(source[j-1])) {
						for end := j + 1; end < len(source); end++ {
							if source[end] == '\\' {
								end++
							} else if source[end] == source[j] {
								j = end
								break
							}
						}
					}
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						destination := strings.TrimSpace(source[i+2 : j])
						return labelEnd, j + 1, destination == "" || destination == "<>"
					}
				}
			}
			return 0, 0, false
		}
	}
	return 0, 0, false
}
