package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestBacklogAssignmentThenPromotionEnqueuesOnce(t *testing.T) {
	seedTestCatalog(t)
	agentID := seededReadyAgentID(t)
	for _, handoff := range []bool{false, true} {
		for _, concurrent := range []bool{false, true} {
			t.Run(fmt.Sprintf("handoff=%t/concurrent=%t", handoff, concurrent), func(t *testing.T) {
				projectID := createWorkflowTestProject(t, "Assignment promotion")
				wf := createTestWorkflow(t, "Assignment promotion "+workflowTestSuffix(), "todo")
				setProjectWorkflow(t, projectID, map[string]any{"workflow_id": wf.ID}).Want(http.StatusOK)
				if handoff {
					setColumnHandoff(t, projectID, "todo", map[string]any{
						"handler": map[string]any{"type": "agent", "id": agentID},
					}).Want(http.StatusOK)
				}
				issueID := dbfx.Issue(t, "Park, then promote", testutil.Cols{"status": "backlog", "project_id": projectID})
				t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID) })
				updateIssueForTest(t, issueID, map[string]any{"assignee_type": "agent", "assignee_id": agentID}).Want(http.StatusOK)
				if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID); n != 0 {
					t.Fatalf("backlog assignment started %d runs", n)
				}
				writers := 1
				if concurrent {
					writers = 8
				}
				responses := make([]*httptest.ResponseRecorder, writers)
				var wg sync.WaitGroup
				start := make(chan struct{})
				for i := range responses {
					wg.Add(1)
					go func() {
						defer wg.Done()
						req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{"status": "todo"}), "id", issueID)
						responses[i] = httptest.NewRecorder()
						<-start
						testHandler.UpdateIssue(responses[i], req)
					}()
				}
				close(start)
				wg.Wait()
				for _, response := range responses {
					if response.Code != http.StatusOK {
						t.Fatalf("promotion: %d %s", response.Code, response.Body.String())
					}
				}
				if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issueID, agentID); n != 1 {
					t.Fatalf("assignment then promotion started %d runs, want exactly one", n)
				}
			})
		}
	}
}
