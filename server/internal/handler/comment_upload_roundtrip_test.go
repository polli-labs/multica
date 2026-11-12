package handler

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestCommentFreshUploadsBindOnceAndDownloadIntact(t *testing.T) {
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	h := *testHandler
	h.Storage = storage.NewLocalStorageFromEnv()
	for _, threaded := range []bool{false, true} {
		for _, count := range []int{1, 5} {
			t.Run(fmt.Sprintf("threaded=%t/files=%d", threaded, count), func(t *testing.T) {
				issueID := dbfx.Issue(t, "Fresh proof uploads")
				body := map[string]any{"content": "proof"}
				initialComments := 0
				if threaded {
					body["parent_id"] = dbfx.Comment(t, issueID, "proof goes here")
					initialComments = 1
				}
				var ids []string
				payloads := map[string][]byte{}
				for i := 0; i < count; i++ {
					var upload bytes.Buffer
					writer := multipart.NewWriter(&upload)
					name := []string{"proof.png", "proof.json", "proof.html", "proof.mp4", "proof.gif"}[i]
					payload := []byte("unique proof bytes: " + name)
					part, err := writer.CreateFormFile("file", name)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := part.Write(payload); err != nil {
						t.Fatal(err)
					}
					if err := writer.WriteField("issue_id", issueID); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					req := httptest.NewRequest(http.MethodPost, "/api/upload-file", &upload)
					req.Header.Set("Content-Type", writer.FormDataContentType())
					req.Header.Set("X-User-ID", testUserID)
					req.Header.Set("X-Workspace-ID", testWorkspaceID)
					var attachment AttachmentResponse
					testutil.Call(t, h.UploadFile, req).Want(http.StatusOK).JSON(&attachment)
					if attachment.ID == "" {
						t.Fatal("upload did not persist an attachment")
					}
					ids = append(ids, attachment.ID)
					payloads[attachment.ID] = payload
					t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM attachment WHERE id = $1`, attachment.ID) })
				}
				body["attachment_ids"] = ids
				post := func() *testutil.Response {
					req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
					return testutil.Call(t, h.CreateComment, req)
				}
				var comment CommentResponse
				post().Want(http.StatusCreated).JSON(&comment)
				if len(comment.Attachments) != count {
					t.Fatalf("bound %d files, want %d", len(comment.Attachments), count)
				}
				post().Want(http.StatusConflict)
				if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, issueID); n != initialComments+1 {
					t.Fatalf("duplicate post left %d comments", n)
				}
				for _, id := range ids {
					if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1 AND comment_id = $2`, id, comment.ID); n != 1 {
						t.Fatal("attachment was rebound or lost")
					}
					req := withURLParam(newRequest(http.MethodGet, "/api/attachments/"+id+"/download", nil), "id", id)
					response := testutil.Call(t, h.DownloadAttachment, req).Want(http.StatusOK)
					if !bytes.Equal(response.Body.Bytes(), payloads[id]) {
						t.Fatal("download changed uploaded bytes")
					}
				}
			})
		}
	}
}
