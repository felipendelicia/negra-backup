// internal/api/stubs.go
package api

import "net/http"

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request)                     {}
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request)                   {}
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request)                    {}
func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request)    {}
func (s *Server) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {}
