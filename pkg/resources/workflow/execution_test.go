package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func newExecTestHandler(t *testing.T, mux *http.ServeMux) (*ExecutionHandler, func()) {
	t.Helper()
	srv := httptest.NewServer(mux)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}
	return NewExecutionHandler(c), srv.Close
}

func TestNewExecutionHandler(t *testing.T) {
	c, err := client.NewForTesting("https://test.example.invalid", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h := NewExecutionHandler(c)
	if h == nil || h.sdk == nil {
		t.Fatal("NewExecutionHandler returned nil")
	}
}

// --- List ---

func TestExecutionList_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecutionList{
			Count: 2,
			Results: []Execution{
				{ID: "exec-1", State: "SUCCESS"},
				{ID: "exec-2", State: "RUNNING"},
			},
		})
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	result, err := h.List(ExecutionFilters{}, 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Count != 2 || len(result.Results) != 2 {
		t.Errorf("expected 2 results, got %d", len(result.Results))
	}
}

func TestExecutionList_WithWorkflowFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("workflow") != "wf-abc" {
			t.Errorf("expected workflow=wf-abc, got %q", r.URL.Query().Get("workflow"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecutionList{Count: 0, Results: []Execution{}})
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.List(ExecutionFilters{WorkflowID: "wf-abc"}, 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
}

func TestExecutionList_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.List(ExecutionFilters{}, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Get ---

func TestExecutionGet_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Execution{ID: "exec-123", State: "SUCCESS"})
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	exec, err := h.Get("exec-123")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if exec.ID != "exec-123" || exec.State != "SUCCESS" {
		t.Errorf("unexpected execution: %+v", exec)
	}
}

func TestExecutionGet_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.Get("missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Cancel ---

func TestExecutionCancel_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-456/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	err := h.Cancel("exec-456")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
}

func TestExecutionCancel_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-999/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	err := h.Cancel("exec-999")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- ListTasks ---

func TestListTasks_Success(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-1/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		result := TaskExecutionMap{
			"task-a": {ID: "t1", Name: "task-a", State: "SUCCESS", StartedAt: &now},
			"task-b": {ID: "t2", Name: "task-b", State: "RUNNING"},
		}
		json.NewEncoder(w).Encode(result)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	tasks, err := h.ListTasks("exec-1")
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestListTasks_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-bad/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.ListTasks("exec-bad")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListTasks_WithResults(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-1/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Simulate a response where tasks have Result populated
		fmt.Fprint(w, `{
			"js_task": {"id":"t1","name":"js_task","state":"SUCCESS","result":{"answer":42}},
			"dql_task": {"id":"t2","name":"dql_task","state":"SUCCESS","result":null}
		}`)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	tasks, err := h.ListTasks("exec-1")
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}

	// Find the task with a result
	var found bool
	for _, task := range tasks {
		if task.Name == "js_task" {
			found = true
			if task.Result == nil {
				t.Error("expected js_task to have a non-nil Result")
			}
			// Verify the result was deserialized correctly
			m, ok := task.Result.(map[string]any)
			if !ok {
				t.Fatalf("expected Result to be map[string]any, got %T", task.Result)
			}
			if m["answer"] != float64(42) {
				t.Errorf("expected answer=42, got %v", m["answer"])
			}
		}
	}
	if !found {
		t.Error("js_task not found in ListTasks results")
	}
}

// --- GetTaskLog ---

func TestGetTaskLog_JSONString(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-1/tasks/my-task/log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `"line1\nline2\ttabbed\n"`)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	log, err := h.GetTaskLog("exec-1", "my-task")
	if err != nil {
		t.Fatalf("GetTaskLog() error = %v", err)
	}
	if !strings.Contains(log, "line1") || !strings.Contains(log, "line2") {
		t.Errorf("unexpected log content: %q", log)
	}
}

func TestGetTaskLog_RawString(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-2/tasks/raw-task/log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `plain log without quotes`)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	log, err := h.GetTaskLog("exec-2", "raw-task")
	if err != nil {
		t.Fatalf("GetTaskLog() error = %v", err)
	}
	if log != "plain log without quotes" {
		t.Errorf("unexpected log: %q", log)
	}
}

func TestGetTaskLog_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-x/tasks/t/log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetTaskLog("exec-x", "t")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- GetExecutionLog ---

func TestGetExecutionLog_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-1/log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `"workflow execution log line\n"`)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	log, err := h.GetExecutionLog("exec-1")
	if err != nil {
		t.Fatalf("GetExecutionLog() error = %v", err)
	}
	if !strings.Contains(log, "workflow execution log line") {
		t.Errorf("unexpected log: %q", log)
	}
}

func TestGetExecutionLog_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/bad/log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetExecutionLog("bad")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- GetFullExecutionLog ---

func TestGetFullExecutionLog_Success(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-full/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		result := TaskExecutionMap{
			"step1": {ID: "t1", Name: "step1", State: "SUCCESS", StartedAt: &now},
		}
		json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("/platform/automation/v1/executions/exec-full/tasks/step1/log", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `"step1 output\n"`)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	log, err := h.GetFullExecutionLog("exec-full")
	if err != nil {
		t.Fatalf("GetFullExecutionLog() error = %v", err)
	}
	if !strings.Contains(log, "step1") {
		t.Errorf("expected 'step1' in log, got: %q", log)
	}
}

func TestGetFullExecutionLog_NoTasks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-empty/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TaskExecutionMap{})
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	log, err := h.GetFullExecutionLog("exec-empty")
	if err != nil {
		t.Fatalf("GetFullExecutionLog() error = %v", err)
	}
	if log != "" {
		t.Errorf("expected empty log for no tasks, got: %q", log)
	}
}

// twoTaskMux serves an execution with tasks step1 and step2. step1's log
// always succeeds; step2's log answers with step2Status and a JSON error body.
func twoTaskMux(execID string, step2Status int) *http.ServeMux {
	now := time.Now()
	later := now.Add(time.Second)
	base := "/platform/automation/v1/executions/" + execID
	mux := http.NewServeMux()
	mux.HandleFunc(base+"/log", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `"workflow log\n"`)
	})
	mux.HandleFunc(base+"/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TaskExecutionMap{
			"step1": {ID: "t1", Name: "step1", State: "SUCCESS", StartedAt: &now},
			"step2": {ID: "t2", Name: "step2", State: "SUCCESS", StartedAt: &later},
		})
	})
	mux.HandleFunc(base+"/tasks/step1/log", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `"step1 output\n"`)
	})
	mux.HandleFunc(base+"/tasks/step2/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(step2Status)
		fmt.Fprint(w, `{"error":{"message":"step2 refused"}}`)
	})
	return mux
}

func TestGetFullExecutionLog_TaskLogFailureIsReturned(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h, cleanup := newExecTestHandler(t, twoTaskMux("exec-partial", status))
			defer cleanup()

			log, err := h.GetFullExecutionLog("exec-partial")

			var taskErr *TaskLogError
			if !errors.As(err, &taskErr) {
				t.Fatalf("expected *TaskLogError, got %T: %v", err, err)
			}
			if taskErr.Total != 2 || len(taskErr.Failed) != 1 || taskErr.Failed[0].Task != "step2" {
				t.Errorf("unexpected failure record: %+v", taskErr)
			}
			if !strings.Contains(err.Error(), "step2") {
				t.Errorf("error does not name the failed task: %v", err)
			}
			// The logs that were fetched are still returned, and the text
			// keeps its inline marker for the task that failed.
			if !strings.Contains(log, "step1 output") {
				t.Errorf("fetched log missing from output: %q", log)
			}
			if !strings.Contains(log, "=== Task: step2 [SUCCESS] ===\n(failed to fetch log:") {
				t.Errorf("inline failure marker missing: %q", log)
			}
		})
	}
}

// A task whose log does not exist is not a failure of the command while some
// other task's log was fetched: the text marks it inline, as before.
func TestGetFullExecutionLog_NotFoundAloneIsNotAnError(t *testing.T) {
	h, cleanup := newExecTestHandler(t, twoTaskMux("exec-404", http.StatusNotFound))
	defer cleanup()

	log, err := h.GetFullExecutionLog("exec-404")
	if err != nil {
		t.Fatalf("expected no error for a lone 404, got %v", err)
	}
	if !strings.Contains(log, "step1 output") || !strings.Contains(log, "(failed to fetch log:") {
		t.Errorf("unexpected log: %q", log)
	}
}

func TestGetFullExecutionLog_NoTaskLogFetchedIsAnError(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-none/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TaskExecutionMap{
			"only": {ID: "t1", Name: "only", State: "SUCCESS", StartedAt: &now},
		})
	})
	mux.HandleFunc("/platform/automation/v1/executions/exec-none/tasks/only/log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetFullExecutionLog("exec-none")
	var taskErr *TaskLogError
	if !errors.As(err, &taskErr) {
		t.Fatalf("expected *TaskLogError when no task log could be fetched, got %T: %v", err, err)
	}
	if taskErr.Total != 1 || len(taskErr.Failed) != 1 {
		t.Errorf("unexpected failure record: %+v", taskErr)
	}
}

func TestGetCompleteExecutionLog_TaskLogFailureKeepsFetchedLogs(t *testing.T) {
	h, cleanup := newExecTestHandler(t, twoTaskMux("exec-all", http.StatusForbidden))
	defer cleanup()

	log, err := h.GetCompleteExecutionLog("exec-all")

	var taskErr *TaskLogError
	if !errors.As(err, &taskErr) {
		t.Fatalf("expected *TaskLogError, got %T: %v", err, err)
	}
	if !strings.Contains(log, "=== Workflow Execution Log ===\nworkflow log") || !strings.Contains(log, "step1 output") {
		t.Errorf("fetched logs missing from output: %q", log)
	}
}

// --- GetCompleteExecutionLog ---

func TestGetCompleteExecutionLog_Success(t *testing.T) {
	now := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-complete/log", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `"workflow log\n"`)
	})
	mux.HandleFunc("/platform/automation/v1/executions/exec-complete/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TaskExecutionMap{
			"taskA": {ID: "ta", Name: "taskA", State: "SUCCESS", StartedAt: &now},
		})
	})
	mux.HandleFunc("/platform/automation/v1/executions/exec-complete/tasks/taskA/log", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `"taskA output\n"`)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	log, err := h.GetCompleteExecutionLog("exec-complete")
	if err != nil {
		t.Fatalf("GetCompleteExecutionLog() error = %v", err)
	}
	if !strings.Contains(log, "workflow log") || !strings.Contains(log, "taskA") {
		t.Errorf("unexpected log: %q", log)
	}
}

// --- sortTasksByStartTime ---

func TestSortTasksByStartTime(t *testing.T) {
	t1 := time.Now()
	t2 := t1.Add(time.Second)
	t3 := t1.Add(2 * time.Second)

	tasks := []TaskExecution{
		{Name: "c", StartedAt: &t3},
		{Name: "a", StartedAt: &t1},
		{Name: "nil1", StartedAt: nil},
		{Name: "b", StartedAt: &t2},
	}

	sortTasksByStartTime(tasks)

	if tasks[0].Name != "a" || tasks[1].Name != "b" || tasks[2].Name != "c" {
		t.Errorf("unexpected sort order: %v", func() []string {
			names := make([]string, len(tasks))
			for i, t := range tasks {
				names[i] = t.Name
			}
			return names
		}())
	}
	// nil should be last
	if tasks[len(tasks)-1].StartedAt != nil {
		t.Error("expected nil StartedAt to be last")
	}
}

// --- GetTaskResult ---

func TestGetTaskResult_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-1/tasks/rca_analysis/result", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: got %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "1"}}})
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	result, err := h.GetTaskResult("exec-1", "rca_analysis")
	if err != nil {
		t.Fatalf("GetTaskResult() error = %v", err)
	}
	if result == nil {
		t.Fatal("GetTaskResult() returned nil")
	}
}

func TestGetTaskResult_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-bad/tasks/missing/result", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetTaskResult("exec-bad", "missing")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestGetTaskResult_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions/exec-err/tasks/t1/result", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h, cleanup := newExecTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetTaskResult("exec-err", "t1")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
