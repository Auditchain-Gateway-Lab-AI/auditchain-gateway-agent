package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"time"

	"github.com/go-pdf/fpdf"
	"go-blockchain-api/internal/modules/audit"
)

type Service interface {
	GenerateCSVReport(clientID string, fromTime, toTime time.Time) ([]byte, error)
	GeneratePDFReport(clientID string, fromTime, toTime time.Time) ([]byte, error)
}

type reportService struct {
	auditSvc audit.Service
}

func NewService(auditSvc audit.Service) Service {
	return &reportService{
		auditSvc: auditSvc,
	}
}

func (s *reportService) GenerateCSVReport(clientID string, fromTime, toTime time.Time) ([]byte, error) {
	// Fetch all logs for the given client and time range
	// We'll use a large page size to fetch all or paginate through them.
	// For simplicity in Phase 1, we assume pulling up to 10000 records is fine.

	result, err := s.auditSvc.GetRecentLogsPaginated(clientID, 1, 10000, "", "asc", "", "", &fromTime, &toTime)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	// Add UTF-8 BOM
	buf.WriteString("\xEF\xBB\xBF")

	writer := csv.NewWriter(&buf)

	// Write Header
	header := []string{"Timestamp", "Actor", "Action", "Resource", "Source Table", "Hash Value", "Status", "Blockchain TX ID"}
	if err := writer.Write(header); err != nil {
		return nil, err
	}

	// Write Data
	for _, item := range result.Data {
		txID := ""
		if item.BlockchainTxID != nil {
			txID = *item.BlockchainTxID
		}

		record := []string{
			item.Timestamp.Format(time.RFC3339),
			item.Actor,
			item.Action,
			item.Resource,
			item.SourceSystem, // or SourceTable if you map it
			item.HashValue,
			item.IntegrityStatus,
			txID,
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (s *reportService) GeneratePDFReport(clientID string, fromTime, toTime time.Time) ([]byte, error) {
	// Fetch logs but limit to max 500 for PDF to avoid massive files and memory issues
	result, err := s.auditSvc.GetRecentLogsPaginated(clientID, 1, 500, "", "desc", "", "", &fromTime, &toTime)
	if err != nil {
		return nil, err
	}

	totalLogs := 0
	totalSuccess := 0
	totalPending := 0
	totalIssues := 0

	for _, item := range result.Data {
		totalLogs++
		status := item.IntegrityStatus
		if status == "valid" || status == "success" {
			totalSuccess++
		} else if status == "tampered" || status == "invalid" || status == "failed" {
			totalIssues++
		} else {
			totalPending++
		}
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()

	// Header
	pdf.SetFont("Arial", "B", 16)
	pdf.Cell(40, 10, "Auditchain Gateway - Audit Integrity Report")
	pdf.Ln(10)

	pdf.SetFont("Arial", "", 10)
	periodStr := fmt.Sprintf("Period: %s to %s", fromTime.Format("2006-01-02 15:04"), toTime.Format("2006-01-02 15:04"))
	pdf.Cell(40, 10, periodStr)
	pdf.Ln(6)

	genStr := fmt.Sprintf("Generated At: %s", time.Now().Format("2006-01-02 15:04:05"))
	pdf.Cell(40, 10, genStr)
	pdf.Ln(15)

	// Summary
	pdf.SetFont("Arial", "B", 12)
	pdf.Cell(40, 10, "Summary")
	pdf.Ln(8)

	pdf.SetFont("Arial", "", 10)
	pdf.Cell(40, 6, fmt.Sprintf("Total Logs (Shown): %d", totalLogs))
	pdf.Ln(6)
	pdf.Cell(40, 6, fmt.Sprintf("Total Success/Valid: %d", totalSuccess))
	pdf.Ln(6)
	pdf.Cell(40, 6, fmt.Sprintf("Total Issues/Tampered: %d", totalIssues))
	pdf.Ln(6)
	pdf.Cell(40, 6, fmt.Sprintf("Total Pending: %d", totalPending))
	pdf.Ln(15)

	// Table Header
	pdf.SetFont("Arial", "B", 9)
	colW := []float64{35, 25, 25, 45, 20, 40}
	headers := []string{"Timestamp", "Actor", "Action", "Resource", "Status", "TX ID"}

	for i, h := range headers {
		pdf.CellFormat(colW[i], 7, h, "1", 0, "C", false, 0, "")
	}
	pdf.Ln(-1)

	// Table Data
	pdf.SetFont("Arial", "", 8)
	for _, item := range result.Data {
		txID := ""
		if item.BlockchainTxID != nil {
			txID = *item.BlockchainTxID
			if len(txID) > 16 {
				txID = txID[:16] + "..."
			}
		}

		resource := item.Resource
		if len(resource) > 25 {
			resource = resource[:25] + "..."
		}

		actor := item.Actor
		if len(actor) > 12 {
			actor = actor[:12] + "..."
		}

		action := item.Action
		if len(action) > 12 {
			action = action[:12] + "..."
		}

		pdf.CellFormat(colW[0], 6, item.Timestamp.Format("2006-01-02 15:04:05"), "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW[1], 6, actor, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW[2], 6, action, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW[3], 6, resource, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW[4], 6, item.IntegrityStatus, "1", 0, "C", false, 0, "")
		pdf.CellFormat(colW[5], 6, txID, "1", 0, "L", false, 0, "")
		pdf.Ln(-1)
	}

	if len(result.Data) == 500 {
		pdf.Ln(5)
		pdf.SetFont("Arial", "I", 8)
		pdf.Cell(40, 6, "* Note: PDF is limited to the 500 most recent logs. For complete data, please download the CSV format.")
	}

	var buf bytes.Buffer
	err = pdf.Output(&buf)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
