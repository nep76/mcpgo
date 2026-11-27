package terminal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	"uuid"
)

type Terminal struct {
	Id string

	shell *exec.Cmd

	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	queue chan *Job

	jobs       map[string]*Job
	currentJob *Job

	mutex sync.Mutex
	wg    sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc
}

type Job struct {
	id        string
	endMarker string

	command  string
	status   string
	exitCode int

	output string

	startTime  time.Time
	lastStdout time.Time

	Done chan struct{}
}

type JobResult struct {
	Id       string
	Status   string
	ExitCode int
	Output   *string
}

type TerminalManager struct {
	Terminals map[string]*Terminal
	Mutex     sync.Mutex
}

const EndMarkerFormat = "END MARKER %s"

var st_term = &TerminalManager{Terminals: make(map[string]*Terminal)}

func (j *Job) IsDone() bool {
	select {
	case <-j.Done:
		return true
	default:
		return false
	}
}

func (t *Terminal) WaitForDone(ctx context.Context, timeout time.Duration, job_id string) (string, error) {
	j := t.jobs[job_id]
	if j == nil {
		return "", fmt.Errorf("Invalid job id")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var status string
	var err error
	select {
	case <-j.Done:
		t.mutex.Lock()
		status = j.status
		err = nil
	case <-ctx.Done():
		t.mutex.Lock()
		status = j.status
		err = ctx.Err()
	}
	t.mutex.Unlock()
	return status, err
}

func CreateSession(session_id string, container string, cwd string) (string, error) {
	var new_term Terminal

	if len(st_term.Terminals) >= 10 {
		return "", fmt.Errorf("Too many sessions open")
	}

	if session_id == "" {
		new_term.Id = uuid.New().String()
	} else {
		new_term.Id = session_id
	}

	if st_term.Terminals[new_term.Id] != nil {
		return "", fmt.Errorf("The specified Session-ID already exists.")
	}

	args := []string{"exec", "-i"}
	if cwd != "" {
		args = append(args, "-w", cwd)
	}
	args = append(args, container, "bash", "--norc", "--noprofile")

	new_term.shell = exec.Command("podman", args...)

	stdin, err := new_term.shell.StdinPipe()
	if err != nil {
		return "", err
	}

	stdout, err := new_term.shell.StdoutPipe()
	if err != nil {
		return "", err
	}

	stderr, err := new_term.shell.StderrPipe()
	if err != nil {
		return "", err
	}

	new_term.stdin = stdin
	new_term.stdout = stdout
	new_term.stderr = stderr
	new_term.queue = make(chan *Job, 30)
	new_term.jobs = make(map[string]*Job, 30)
	new_term.ctx, new_term.cancel = context.WithCancel(context.Background())

	if err := new_term.shell.Start(); err != nil {
		return "", err
	}

	go new_term.readOutput(stdout)
	go new_term.readOutput(stderr)
	go new_term.commandWorker()

	st_term.Mutex.Lock()
	defer st_term.Mutex.Unlock()

	st_term.Terminals[new_term.Id] = &new_term

	return new_term.Id, nil
}

func (t *Terminal) commandWorker() {
	t.wg.Add(1)
	defer t.wg.Done()

worker:
	for {
		select {
		case <-t.ctx.Done():
			break worker
		case job := <-t.queue:
			t.mutex.Lock()
			cj := t.currentJob
			t.mutex.Unlock()

			if cj != nil {
				select {
				case <-cj.Done:
				case <-t.ctx.Done():
					break worker
				}
			}

			t.mutex.Lock()

			if t.ctx.Err() != nil || t.shell.ProcessState != nil {
				fmt.Println("Shell is not running?")
				break worker
			}

			t.currentJob = job

			command := "( " + job.command + " ) </dev/null\n"
			_, err := io.WriteString(t.stdin, command)
			if err != nil {
				fmt.Printf("Failed to execute command: %s: %s", job.command, err)
				job.status = "error"
				close(job.Done)
			} else {
				job.status = "running"
			}

			t.mutex.Unlock()
		}
	}
}

func (t *Terminal) readOutput(fd io.Reader) {
	t.wg.Add(1)
	defer t.wg.Done()

	scanner := bufio.NewScanner(fd)

	for scanner.Scan() {
		buf := scanner.Text()

		t.mutex.Lock()
		if t.currentJob != nil && !t.currentJob.IsDone() {
			if strings.HasPrefix(buf, t.currentJob.endMarker) {
				var exit_code int
				_, err := fmt.Sscanf(buf, t.currentJob.endMarker+" %d", &exit_code)
				if err != nil {
					fmt.Println("Failed to parse end-marker?: ", err)
				}
				t.currentJob.exitCode = exit_code
				t.currentJob.status = "done"
				close(t.currentJob.Done)
			} else {
				t.currentJob.output += buf + "\n"
			}
		}
		t.mutex.Unlock()
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("read error:", err)
	}

	t.mutex.Lock()
	if t.currentJob != nil && !t.currentJob.IsDone() {
		close(t.currentJob.Done)
	}
	t.mutex.Unlock()
}

func GetSession(term_id string) *Terminal {
	return st_term.Terminals[term_id]
}

func (t *Terminal) Run(term_id string, cmd string) (string, error) {
	var new_task Job

	if len(cmd) == 0 {
		return "", fmt.Errorf("Command empty")
	}

	new_task.id = uuid.New().String()
	new_task.endMarker = fmt.Sprintf(EndMarkerFormat, new_task.id)
	new_task.command = cmd + "\necho " + new_task.endMarker + " $?"
	new_task.status = "pending"
	new_task.output = ""
	new_task.startTime = time.Now()
	new_task.lastStdout = time.Now()

	new_task.Done = make(chan struct{})

	t.mutex.Lock()
	t.jobs[new_task.id] = &new_task
	t.mutex.Unlock()
	t.queue <- &new_task

	return new_task.id, nil
}

func (t *Terminal) _queryStatus(job_id string) (JobResult, error) {
	if t.jobs[job_id] == nil {
		return JobResult{}, fmt.Errorf("Invalid job id")
	}

	var r = JobResult{
		Id:       t.jobs[job_id].id,
		Status:   t.jobs[job_id].status,
		ExitCode: t.jobs[job_id].exitCode,
		Output:   &t.jobs[job_id].output,
	}

	return r, nil
}

func (t *Terminal) QueryStatus(job_id string) (JobResult, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	return t._queryStatus(job_id)
}

func (t *Terminal) PopResult(job_id string) (JobResult, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	r, err := t._queryStatus(job_id)

	if err != nil {
		return JobResult{}, err
	} else if r.Status == "pending" || r.Status == "running" || r.Status == "seems_hungup" {
		return JobResult{}, fmt.Errorf("")
	}

	if t.jobs[job_id] == t.currentJob {
		t.currentJob = nil
	}

	delete(t.jobs, job_id)

	return r, nil
}

func (t *Terminal) CloseSession() error {
	t.cancel()

	var err error = nil

	if t.shell.Process != nil {
		t.stdin.Close()
		err = t.shell.Wait()
		t.wg.Wait()
	}

	st_term.Mutex.Lock()
	delete(st_term.Terminals, t.Id)
	st_term.Mutex.Unlock()

	return err
}
