package core

import "github.com/gin-gonic/gin"

// APIError is the error envelope used in all non-success responses.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIResponse is the uniform response wrapper for every endpoint:
//
//	{ "success": true,  "data": {...}, "error": null }
//	{ "success": false, "data": null, "error": {"code":"...","message":"..."} }
type APIResponse struct {
	Success bool      `json:"success"`
	Data    any       `json:"data"`
	Error   *APIError `json:"error"`
}

// OK writes a success response with the given payload.
func OK(c *gin.Context, data any) {
	c.JSON(200, APIResponse{Success: true, Data: data, Error: nil})
}

// Created writes a 201 success response.
func Created(c *gin.Context, data any) {
	c.JSON(201, APIResponse{Success: true, Data: data, Error: nil})
}

// Fail writes an error response with the given HTTP status, code and message.
// It aborts the request so later handlers/middleware do not run.
func Fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, APIResponse{
		Success: false,
		Data:    nil,
		Error:   &APIError{Code: code, Message: message},
	})
}
