package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/vision"
)

// maxImagesPerCall bounds a single request. Beyond a handful the upload cost
// outweighs whatever the extra pictures add.
const maxImagesPerCall = 4

// RegisterVision lets Freya look at pictures — including the screen.
//
// The screenshot pairing is the point: desktop_screenshot already existed, but
// without vision it produced a file she could not read. Together they let her
// answer "what does this error say" about something only visible on screen.
func RegisterVision(r *Registry, analyzer llm.VisionAnalyzer) {
	if analyzer == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "image_view",
			Description: "Look at an image and answer a question about it. Handles " +
				"screenshots, photographs, diagrams, charts, scanned pages and " +
				"handwriting. Use it whenever the answer is in a picture rather than " +
				"in text — including after desktop_screenshot, to read what is on screen.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "Image file. Several may be given, " +
					"comma separated, to compare them."},
				"question": {Type: "string", Description: "What you want to know. Be specific: " +
					"'read the error message' beats 'describe this'."},
			}, "path"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			raw := argString(args, "path")
			if raw == "" {
				return "", fmt.Errorf("path is required")
			}
			question := argString(args, "question")
			if question == "" {
				question = "Describe this image in detail. If it contains text, read it out exactly."
			}

			var paths []string
			for _, p := range strings.Split(raw, ",") {
				if p = strings.TrimSpace(p); p != "" {
					paths = append(paths, expand(p))
				}
			}
			if len(paths) > maxImagesPerCall {
				return "", fmt.Errorf("at most %d images per call, got %d",
					maxImagesPerCall, len(paths))
			}

			var images [][]byte
			var mimes []string
			var notes []string
			for _, p := range paths {
				if _, err := os.Stat(p); err != nil {
					return "", fmt.Errorf("no such image: %s", p)
				}
				if !vision.Supported(p) {
					return "", fmt.Errorf("%s is not a supported image format",
						filepath.Base(p))
				}
				img, err := vision.Prepare(p)
				if err != nil {
					return "", err
				}
				images = append(images, img.Data)
				mimes = append(mimes, img.MimeType)
				notes = append(notes, filepath.Base(p)+": "+img.Describe())
			}

			ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()

			answer, err := analyzer.AnalyzeImage(ctx, question, images, mimes)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("[%s]\n\n%s", strings.Join(notes, "; "), answer), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "screen_read",
			Description: "Take a screenshot and immediately read it. Use when the user " +
				"refers to something on their screen — an error, a dialog, a page they " +
				"are looking at — that you cannot otherwise see.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"question": {Type: "string", Description: "What to look for on screen."},
				"window":   {Type: "boolean", Description: "Capture only the focused window."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if !have("scrot") {
				return "", fmt.Errorf("scrot is not installed; cannot capture the screen")
			}
			shot := filepath.Join(os.TempDir(),
				fmt.Sprintf("freya-screen-%d.png", time.Now().UnixNano()))
			defer os.Remove(shot)

			cmdArgs := []string{shot}
			if argBool(args, "window") {
				cmdArgs = append([]string{"-u"}, cmdArgs...)
			}
			if _, err := run(ctx, 20*time.Second, "scrot", cmdArgs...); err != nil {
				return "", fmt.Errorf("screenshot failed: %w", err)
			}

			img, err := vision.Prepare(shot)
			if err != nil {
				return "", err
			}
			question := argString(args, "question")
			if question == "" {
				question = "Describe what is on this screen. Read any visible text exactly."
			}

			ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()

			answer, err := analyzer.AnalyzeImage(ctx, question,
				[][]byte{img.Data}, []string{img.MimeType})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("[screen: %s]\n\n%s", img.Describe(), answer), nil
		},
	})
}
