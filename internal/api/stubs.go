// internal/api/stubs.go
package api

import "net/http"

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {}
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request)                   {}
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request)                  {}
func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request)                  {}
func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request)                  {}
func (s *Server) handleTriggerJob(w http.ResponseWriter, r *http.Request)                 {}
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request)                   {}
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request)                    {}
func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request)    {}
func (s *Server) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {}
