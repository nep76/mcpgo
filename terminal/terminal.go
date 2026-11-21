package terminal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
	"uuid"
)

type Terminal struct {
	Id string

	Shell *exec.Cmd

	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser

	Queue chan *Job

	Jobs       map[string]*Job
	CurrentJob *Job

	Mutex sync.Mutex
	Wg    sync.WaitGroup

	Ctx    context.Context
	Cancel context.CancelFunc
}

type Job struct {
	Id        string
	EndMarker string

	Command string
	Status  string

	Output string

	StartTime  time.Time
	LastStdout time.Time

	Reuslt int

	Done chan struct{}
}

type JobResult struct {
	Id string

	Status string
	Output *string
}

type TerminalManager struct {
	Terminals map[string]*Terminal
	Mutex     sync.Mutex
}

const EndMarkerFormat = "END MARKER %s"

var st_term = &TerminalManager{Terminals: make(map[string]*Terminal)}

func (j *Job) isDone() bool {
	select {
	case <-j.Done:
		return true
	default:
		return false
	}
}

func CreateSession(container string) (string, error) {
	var new_term Terminal

	if len(st_term.Terminals) >= 10 {
		return "", fmt.Errorf("Too many sessions open")
	}

	new_term.Shell = exec.Command(
		"podman",
		"exec",
		"-i",
		container,
		"bash",
		"--norc",
		"--noprofile",
	)

	stdin, err := new_term.Shell.StdinPipe()
	if err != nil {
		return "", err
	}

	stdout, err := new_term.Shell.StdoutPipe()
	if err != nil {
		return "", err
	}

	stderr, err := new_term.Shell.StderrPipe()
	if err != nil {
		return "", err
	}

	new_term.Stdin = stdin
	new_term.Stdout = stdout
	new_term.Stderr = stderr
	new_term.Queue = make(chan *Job, 30)
	new_term.Jobs = make(map[string]*Job, 30)
	new_term.Ctx, new_term.Cancel = context.WithCancel(context.Background())

	if err := new_term.Shell.Start(); err != nil {
		return "", err
	}

	go new_term.readOutput(stdout)
	go new_term.readOutput(stderr)
	go new_term.commandWorker()

	st_term.Mutex.Lock()
	defer st_term.Mutex.Unlock()

	new_term.Id = uuid.New().String()
	st_term.Terminals[new_term.Id] = &new_term

	return new_term.Id, nil
}

func (t *Terminal) commandWorker() {
	t.Wg.Add(1)
	defer t.Wg.Done()

worker:
	for {
		select {
		case <-t.Ctx.Done():
			break worker
		case job := <-t.Queue:
			t.Mutex.Lock()
			cj := t.CurrentJob
			t.Mutex.Unlock()

			if cj != nil {
				select {
				case <-cj.Done:
				case <-t.Ctx.Done():
					break worker
				}
			}

			t.Mutex.Lock()

			if t.Ctx.Err() != nil || t.Shell.ProcessState != nil {
				fmt.Println("Shell is not running?")
				break worker
			}

			t.CurrentJob = job

			command := "( " + job.Command + " ) </dev/null\n"
			_, err := io.WriteString(t.Stdin, command)
			if err != nil {
				fmt.Println("Failed to execute command: ", job.Command)
				job.Status = "error"
				close(job.Done)
			} else {
				job.Status = "running"
			}

			t.Mutex.Unlock()
		}
	}
}

func (t *Terminal) readOutput(fd io.Reader) {
	t.Wg.Add(1)
	defer t.Wg.Done()

	scanner := bufio.NewScanner(fd)

	for scanner.Scan() {
		buf := scanner.Text()

		t.Mutex.Lock()
		if t.CurrentJob != nil && !t.CurrentJob.isDone() {
			if buf == t.CurrentJob.EndMarker {
				t.CurrentJob.Status = "done"
				close(t.CurrentJob.Done)
			} else {
				t.CurrentJob.Output += buf + "\n"
			}
		}
		t.Mutex.Unlock()
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("read error:", err)
	}

	t.Mutex.Lock()
	if t.CurrentJob != nil && !t.CurrentJob.isDone() {
		close(t.CurrentJob.Done)
	}
	t.Mutex.Unlock()
}

func GetSession(term_id string) *Terminal {
	return st_term.Terminals[term_id]
}

func (t *Terminal) Run(term_id string, cmd string) (string, error) {
	var new_task Job

	if len(cmd) == 0 {
		return "", fmt.Errorf("Command empty")
	}

	new_task.Id = uuid.New().String()
	new_task.EndMarker = fmt.Sprintf(EndMarkerFormat, new_task.Id)
	new_task.Command = cmd + "\necho " + new_task.EndMarker
	new_task.Status = "pending"
	new_task.Output = ""
	new_task.StartTime = time.Now()
	new_task.LastStdout = time.Now()
	new_task.Reuslt = 0

	new_task.Done = make(chan struct{})

	t.Mutex.Lock()
	t.Jobs[new_task.Id] = &new_task
	t.Mutex.Unlock()
	t.Queue <- &new_task

	return new_task.Id, nil
}

func (t *Terminal) _queryStatus(job_id string) (JobResult, error) {
	if t.Jobs[job_id] == nil {
		return JobResult{}, fmt.Errorf("Invalid job id")
	}

	var r = JobResult{
		Id:     t.Jobs[job_id].Id,
		Status: t.Jobs[job_id].Status,
		Output: &t.Jobs[job_id].Output,
	}

	return r, nil
}

func (t *Terminal) QueryStatus(job_id string) (JobResult, error) {
	t.Mutex.Lock()
	defer t.Mutex.Unlock()
	return t._queryStatus(job_id)
}

func (t *Terminal) PopResult(job_id string) (JobResult, error) {
	t.Mutex.Lock()
	defer t.Mutex.Unlock()
	r, err := t._queryStatus(job_id)

	if err != nil {
		return JobResult{}, err
	} else if r.Status == "pending" || r.Status == "running" || r.Status == "seems_hungup" {
		return JobResult{}, fmt.Errorf("")
	}

	if t.Jobs[job_id] == t.CurrentJob {
		t.CurrentJob = nil
	}

	delete(t.Jobs, job_id)

	return r, nil
}

func (t *Terminal) CloseSession() error {
	t.Cancel()

	var err error = nil

	if t.Shell.Process != nil {
		t.Stdin.Close()
		err = t.Shell.Wait()
		t.Wg.Wait()
	}

	st_term.Mutex.Lock()
	delete(st_term.Terminals, t.Id)
	st_term.Mutex.Unlock()

	return err
}
