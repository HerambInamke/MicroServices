package com.provenance.catalog;

import java.util.stream.Collectors;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.http.converter.HttpMessageNotReadableException;
import org.springframework.web.HttpRequestMethodNotSupportedException;
import org.springframework.web.bind.MethodArgumentNotValidException;
import org.springframework.web.bind.MissingServletRequestParameterException;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;
import org.springframework.web.method.annotation.MethodArgumentTypeMismatchException;
import org.springframework.web.servlet.resource.NoResourceFoundException;

/** Maps every failure onto the standard error envelope. */
@RestControllerAdvice
public class GlobalExceptionHandler {

    private static final Logger log = LoggerFactory.getLogger(GlobalExceptionHandler.class);

    private static ResponseEntity<ApiResponse> error(HttpStatus status, String code, String message) {
        return ResponseEntity.status(status).body(ApiResponse.error(code, message));
    }

    @ExceptionHandler(ApiException.class)
    ResponseEntity<ApiResponse> api(ApiException e) {
        return error(e.status(), e.code(), e.getMessage());
    }

    @ExceptionHandler(MethodArgumentNotValidException.class)
    ResponseEntity<ApiResponse> invalid(MethodArgumentNotValidException e) {
        String msg = e.getBindingResult().getFieldErrors().stream()
                .map(f -> f.getField() + " " + f.getDefaultMessage())
                .sorted()
                .collect(Collectors.joining("; "));
        return error(HttpStatus.BAD_REQUEST, "VALIDATION_ERROR", msg);
    }

    @ExceptionHandler(HttpMessageNotReadableException.class)
    ResponseEntity<ApiResponse> unreadable(HttpMessageNotReadableException e) {
        return error(HttpStatus.BAD_REQUEST, "INVALID_BODY", "request body must be valid JSON of the expected shape");
    }

    @ExceptionHandler({MethodArgumentTypeMismatchException.class, MissingServletRequestParameterException.class})
    ResponseEntity<ApiResponse> badParam(Exception e) {
        return error(HttpStatus.BAD_REQUEST, "INVALID_PARAMETER", e.getMessage());
    }

    @ExceptionHandler(NoResourceFoundException.class)
    ResponseEntity<ApiResponse> noRoute(NoResourceFoundException e) {
        return error(HttpStatus.NOT_FOUND, "NOT_FOUND", "no such route");
    }

    @ExceptionHandler(HttpRequestMethodNotSupportedException.class)
    ResponseEntity<ApiResponse> method(HttpRequestMethodNotSupportedException e) {
        return error(HttpStatus.METHOD_NOT_ALLOWED, "METHOD_NOT_ALLOWED", e.getMessage());
    }

    @ExceptionHandler(Exception.class)
    ResponseEntity<ApiResponse> unexpected(Exception e) {
        log.error("unhandled error", e);
        return error(HttpStatus.INTERNAL_SERVER_ERROR, "INTERNAL", "something went wrong");
    }
}
