package com.provenance.catalog;

import org.springframework.http.HttpStatus;

/** The authenticated caller, taken from the identity headers the gateway sets after verifying the JWT. */
public record Caller(String userId, String role) {

    public static Caller of(String userId, String role) {
        if (userId == null || userId.isBlank()) {
            throw new ApiException(HttpStatus.UNAUTHORIZED, "UNAUTHENTICATED", "authentication required");
        }
        return new Caller(userId, role == null ? "" : role);
    }

    public void requireRole(String required) {
        if (!required.equals(role)) {
            throw new ApiException(HttpStatus.FORBIDDEN, "FORBIDDEN", "only a " + required + " can do this");
        }
    }

    public void requireOwner(String ownerId) {
        if (!userId.equals(ownerId)) {
            throw new ApiException(HttpStatus.FORBIDDEN, "FORBIDDEN", "you can only change your own listings");
        }
    }
}
