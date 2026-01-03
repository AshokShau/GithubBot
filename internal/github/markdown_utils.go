package github

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PaulSonOfLars/gotgbot/v2"
)

// ConvertHTMLToMarkdown converts HTML to Markdown using the html-to-markdown library.
func ConvertHTMLToMarkdown(html string) string {
	conv := converter.NewConverter(
		converter.WithPlugins(
			strikethrough.NewStrikethroughPlugin(),
			table.NewTablePlugin(),
		),
	)

	markdown, err := conv.ConvertString(html)
	if err != nil {
		log.Println("Error converting HTML to Markdown:", err)
		return html
	}

	return markdown
}

// EscapeMarkdownV2 escapes characters for Telegram's MarkdownV2 format.
func EscapeMarkdownV2(text string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\", // Escape backslash first!
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}

// EscapeMarkdownV2URL escapes characters for the URL part of a MarkdownV2 link.
func EscapeMarkdownV2URL(text string) string {
	replacer := strings.NewReplacer(
		"(", "\\(",
		")", "\\)",
	)
	return replacer.Replace(text)
}

// FormatTextWithMarkdown preserves Markdown links and code blocks while escaping other special characters.
func FormatTextWithMarkdown(text string) string {
	emailRe := regexp.MustCompile(`<[^> ]+@[^> ]+>`)
	var emails []string
	protectedText := emailRe.ReplaceAllStringFunc(text, func(m string) string {
		emails = append(emails, m)
		return fmt.Sprintf("___EMAIL_PLACEHOLDER_%d___", len(emails)-1)
	})

	markdownText := ConvertHTMLToMarkdown(protectedText)

	for i, email := range emails {
		placeholder := fmt.Sprintf("___EMAIL_PLACEHOLDER_%d___", i)
		markdownText = strings.Replace(markdownText, placeholder, email, -1)
	}

	re := regexp.MustCompile("(?s)\\[[^\\]]+\\]\\([^\\)]+\\)|`[^`]+`|```.+?```")

	var originals []string
	tempBody := re.ReplaceAllStringFunc(markdownText, func(match string) string {
		originals = append(originals, match)
		return fmt.Sprintf("___PLACEHOLDER_%d___", len(originals)-1)
	})

	escapedBody := EscapeMarkdownV2(tempBody)
	for i, original := range originals {
		placeholder := fmt.Sprintf("___PLACEHOLDER_%d___", i)
		escapedPlaceholder := EscapeMarkdownV2(placeholder)
		escapedBody = strings.Replace(escapedBody, escapedPlaceholder, original, 1)
	}

	return escapedBody
}

func FormatReleaseBody(body string) string {
	formattedText := FormatTextWithMarkdown(body)
	lines := strings.Split(formattedText, "\n")
	const maxLines = 10
	const maxChars = 800
	isLong := len(lines) > maxLines || len(formattedText) > maxChars

	var finalBody strings.Builder

	if !isLong {
		for _, line := range lines {
			finalBody.WriteString(">" + line + "\n")
		}
		return strings.TrimSuffix(finalBody.String(), "\n")
	}

	splitIndex := 5

	for i := 0; i < splitIndex && i < len(lines); i++ {
		finalBody.WriteString(">" + lines[i] + "\n")
	}

	finalBody.WriteString("||\n")

	for i := splitIndex; i < len(lines); i++ {
		finalBody.WriteString(">" + lines[i] + "\n")
	}

	return strings.TrimSuffix(finalBody.String(), "\n") + "||"
}

func FormatRepo(repoFullName string) string {
	return fmt.Sprintf("[%s](https://github.com/%s)", EscapeMarkdownV2(repoFullName), EscapeMarkdownV2URL(repoFullName))
}

func FormatUser(userLogin string) string {
	return fmt.Sprintf("[%s](https://github.com/%s)", EscapeMarkdownV2(userLogin), EscapeMarkdownV2URL(userLogin))
}

func FormatMessageWithButton(message, buttonText, buttonURL string) (string, *gotgbot.InlineKeyboardMarkup) {
	if buttonText == "" || buttonURL == "" {
		return message, nil
	}
	return message, &gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{{Text: buttonText, Url: buttonURL}},
		},
	}
}
