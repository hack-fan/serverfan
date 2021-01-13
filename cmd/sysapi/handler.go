package main

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/hack-fan/skadi/job"
	"github.com/hack-fan/skadi/types"
)

type Handler struct {
	js *job.Service
}

func NewHandler(js *job.Service) *Handler {
	return &Handler{
		js: js,
	}
}

func (h *Handler) PostJob(c echo.Context) error {
	var req = new(types.JobInput)
	err := c.Bind(req)
	if err != nil {
		return err
	}
	err = h.js.Push(req)
	if err != nil {
		return err
	}
	return c.NoContent(204)
}

// API status
func getStatus(c echo.Context) error {
	return c.NoContent(http.StatusNoContent)
}
