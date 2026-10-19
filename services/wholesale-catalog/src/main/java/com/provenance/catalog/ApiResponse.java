package com.provenance.catalog;

import java.util.Map;

/** The {data, error, meta} envelope every endpoint returns (see api-architecture.md). */
public record ApiResponse(Object data, ApiError error, Map<String, Object> meta) {

    public record ApiError(String code, String message) {}

    public static ApiResponse ok(Object data) {
        return new ApiResponse(data, null, Map.of());
    }

    public static ApiResponse ok(Object data, Map<String, Object> meta) {
        return new ApiResponse(data, null, meta);
    }

    public static ApiResponse error(String code, String message) {
        return new ApiResponse(null, new ApiError(code, message), Map.of());
    }
}
