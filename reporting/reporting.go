package reporting

import (
	"fmt"
	"os"
	"time"

	"github.com/PlakarKorp/plakar/logging"
	"github.com/PlakarKorp/plakar/objects"
	"github.com/PlakarKorp/plakar/repository"
	"github.com/PlakarKorp/plakar/snapshot"
)

const PLAKAR_API_URL = "https://api.plakar.io/v1/reporting/reports"

type Emitter interface {
	Emit(report Report, logger *logging.Logger)
}

type Reporter struct {
	logger            *logging.Logger
	emitter           Emitter
	currentTask       *ReportTask
	currentRepository *ReportRepository
	currentSnapshot   *ReportSnapshot
}

func NewReporter(reporting bool, logger *logging.Logger) *Reporter {
	if logger == nil {
		logger = logging.NewLogger(os.Stdout, os.Stderr)
	}

	var emitter Emitter

	if !reporting {
		emitter = &NullEmitter{}
	} else {

		url := os.Getenv("PLAKAR_API_URL")
		if url == "" {
			url = PLAKAR_API_URL
		}

		emitter = &HttpEmitter{
			url:   url,
			token: os.Getenv("PLAKAR_API_TOKEN"),
			retry: 3,
		}
	}

	return &Reporter{
		logger:  logger,
		emitter: emitter,
	}
}

func (reporter *Reporter) TaskStart(kind string, name string) {
	if reporter.currentTask != nil {
		reporter.logger.Warn("already in a task")
	}

	reporter.currentTask = &ReportTask{
		StartTime: time.Now(),
		Type:      kind,
		Name:      name,
	}
}

func (reporter *Reporter) WithRepositoryName(name string) {
	if reporter.currentRepository != nil {
		reporter.logger.Warn("already has a repository")
	}
	reporter.currentRepository = &ReportRepository{
		Name: name,
	}
}

func (reporter *Reporter) WithRepository(repository *repository.Repository) {
	configuration := repository.Configuration()
	reporter.currentRepository.Storage = configuration
}

func (reporter *Reporter) WithSnapshotID(repository *repository.Repository, snapshotId objects.MAC) {
	snap, err := snapshot.Load(repository, snapshotId)
	if err != nil {
		reporter.logger.Warn("failed to load snapshot: %s", err)
		return
	}
	reporter.WithSnapshot(snap)
	snap.Close()
}

func (reporter *Reporter) WithSnapshot(snapshot *snapshot.Snapshot) {
	if reporter.currentSnapshot != nil {
		reporter.logger.Warn("already has a snapshot")
	}
	reporter.currentSnapshot = &ReportSnapshot{
		Header: *snapshot.Header,
	}
}

func (reporter *Reporter) TaskDone() {
	reporter.taskEnd(StatusOK, 0, "")
}

func (reporter *Reporter) TaskWarning(errorMessage string, args ...interface{}) {
	reporter.taskEnd(StatusWarning, 0, errorMessage, args...)
}

func (reporter *Reporter) TaskFailed(errorCode TaskErrorCode, errorMessage string, args ...interface{}) {
	reporter.taskEnd(StatusFailed, errorCode, errorMessage, args...)
}

func (reporter *Reporter) taskEnd(status TaskStatus, errorCode TaskErrorCode, errorMessage string, args ...interface{}) {
	reporter.currentTask.Status = status
	reporter.currentTask.ErrorCode = errorCode
	if len(args) == 0 {
		reporter.currentTask.ErrorMessage = errorMessage
	} else {
		reporter.currentTask.ErrorMessage = fmt.Sprintf(errorMessage, args...)
	}
	reporter.currentTask.Duration = time.Since(reporter.currentTask.StartTime)

	report := Report{
		Timestamp:  time.Now(),
		Task:       reporter.currentTask,
		Repository: reporter.currentRepository,
		Snapshot:   reporter.currentSnapshot,
	}

	reporter.currentTask = nil
	reporter.currentRepository = nil
	reporter.currentSnapshot = nil
	go reporter.emitter.Emit(report, reporter.logger)
}
