package clients

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"net/http"
	"time"
)

var reportingClient *ReportingService

type ReportingService struct {
	url    string
	client *http.Client
}

func InitializeReportingClient(url string, timeout time.Duration) {
	reportingClient = &ReportingService{
		url: url, client: &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
	}
}

type SequencerAlert struct {
	ProcessName string `json:"process_name"`
	ErrorMsg    string `json:"error_msg"`
	Timestamp   string `json:"timestamp"`
	Severity    string `json:"severity"`
}

func (s SequencerAlert) String() string {
	return fmt.Sprintf("ProcessName: %s, ErrorMsg: %s, Timestamp: %s, Severity: %s",
		s.ProcessName, s.ErrorMsg, s.Timestamp, s.Severity)
}

// sendPostRequest sends a POST request to the specified URL
func SendFailureNotification(processName, errorMsg, timestamp, severity string) {

	issue := SequencerAlert{
		processName,
		errorMsg,
		timestamp,
		severity,
	}

	jsonData, err := json.Marshal(issue)
	if err != nil {
		log.Errorln("Unable to marshal notification: ", issue)
		return
	}
	req, err := http.NewRequest("POST", reportingClient.url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Errorln("Error creating request: ", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send the request
	resp, err := reportingClient.client.Do(req)
	if err != nil {
		log.Errorf("Error sending request for issue %s: %s\n", issue.String(), err)
		// Handle error in case of failure
		return
	}
	defer resp.Body.Close()

	// Here you can handle response or further actions
	log.Debugln("Reporting service response status: ", resp.Status)
}
