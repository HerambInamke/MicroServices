package com.provenance.catalog;

import jakarta.validation.Valid;
import jakarta.validation.constraints.DecimalMin;
import jakarta.validation.constraints.Digits;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Size;
import java.math.BigDecimal;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import org.springframework.data.domain.Page;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PatchMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class ProductController {

    private static final int MAX_QTY = 1_000_000;

    public record CreateProduct(
            @NotBlank @Size(max = 200) String name,
            @Size(max = 2000) String description,
            @NotNull Long categoryId,
            @NotNull @Min(1) Integer moq,
            @NotNull @DecimalMin("0.01") @Digits(integer = 10, fraction = 2) BigDecimal basePrice) {}

    public record UpdateProduct(
            @Size(max = 200) String name,
            @Size(max = 2000) String description,
            Long categoryId,
            @Min(1) Integer moq,
            @DecimalMin("0.01") @Digits(integer = 10, fraction = 2) BigDecimal basePrice) {}

    public record Tier(
            @NotNull @Min(1) Integer minQty,
            @NotNull @DecimalMin("0.01") @Digits(integer = 10, fraction = 2) BigDecimal unitPrice) {}

    public record SetTiers(@NotNull @Size(max = 20) List<@Valid @NotNull Tier> tiers) {}

    public record ProductView(
            Long id, String wholesalerId, String name, String description, Long categoryId,
            Integer moq, BigDecimal basePrice, List<Tier> priceTiers, Instant createdAt, Instant updatedAt) {

        static ProductView of(Product p) {
            List<Tier> tiers = p.getPriceTiers().stream().map(t -> new Tier(t.getMinQty(), t.getUnitPrice())).toList();
            return new ProductView(p.getId(), p.getWholesalerId(), p.getName(), p.getDescription(), p.getCategoryId(),
                    p.getMoq(), p.getBasePrice(), tiers, p.getCreatedAt(), p.getUpdatedAt());
        }
    }

    private final ProductService service;
    private final EventPublisher events;

    public ProductController(ProductService service, EventPublisher events) {
        this.service = service;
        this.events = events;
    }

    @GetMapping("/products")
    public ApiResponse list(
            @RequestParam(required = false) String wholesalerId,
            @RequestParam(required = false) Long categoryId,
            @RequestParam(defaultValue = "1") int page,
            @RequestParam(defaultValue = "20") int limit) {
        if (page < 1 || limit < 1 || limit > 100) {
            throw ApiException.badRequest("INVALID_PAGING", "page must be >= 1 and limit between 1 and 100");
        }
        Page<Product> result = service.list(wholesalerId, categoryId, page, limit);
        Map<String, Object> meta = Map.of("page", page, "limit", limit,
                "total", result.getTotalElements(), "hasMore", result.hasNext());
        return ApiResponse.ok(result.map(ProductView::of).getContent(), meta);
    }

    @GetMapping("/products/{id}")
    public ApiResponse get(@PathVariable Long id) {
        return ApiResponse.ok(ProductView.of(service.get(id)));
    }

    /** Server-side pricing used by Order/Payment, so clients can never set their own price. */
    @GetMapping("/products/{id}/quote")
    public ApiResponse quote(@PathVariable Long id, @RequestParam int qty) {
        if (qty < 1 || qty > MAX_QTY) {
            throw ApiException.badRequest("INVALID_QUANTITY", "qty must be between 1 and " + MAX_QTY);
        }
        return ApiResponse.ok(service.quote(id, qty));
    }

    @PostMapping("/products")
    @ResponseStatus(HttpStatus.CREATED)
    public ApiResponse create(
            @RequestHeader(name = "X-User-Id", required = false) String userId,
            @RequestHeader(name = "X-User-Role", required = false) String role,
            @Valid @RequestBody CreateProduct req) {
        var fields = new ProductService.ProductFields(req.name(), req.description(), req.categoryId(), req.moq(), req.basePrice());
        // Published here, after the service's transaction has committed.
        Product p = service.create(Caller.of(userId, role), fields);
        events.publish("product.created", "ProductCreated", p.getId().toString(), EventPublisher.productPayload(p));
        return ApiResponse.ok(ProductView.of(p));
    }

    @PatchMapping("/products/{id}")
    public ApiResponse update(
            @RequestHeader(name = "X-User-Id", required = false) String userId,
            @RequestHeader(name = "X-User-Role", required = false) String role,
            @PathVariable Long id,
            @Valid @RequestBody UpdateProduct req) {
        var fields = new ProductService.ProductFields(req.name(), req.description(), req.categoryId(), req.moq(), req.basePrice());
        Product p = service.update(Caller.of(userId, role), id, fields);
        events.publish("product.updated", "ProductUpdated", p.getId().toString(), EventPublisher.productPayload(p));
        return ApiResponse.ok(ProductView.of(p));
    }

    @DeleteMapping("/products/{id}")
    public ApiResponse delete(
            @RequestHeader(name = "X-User-Id", required = false) String userId,
            @RequestHeader(name = "X-User-Role", required = false) String role,
            @PathVariable Long id) {
        service.delete(Caller.of(userId, role), id);
        events.publish("product.deleted", "ProductDeleted", id.toString(), Map.of("productId", id));
        return ApiResponse.ok(Map.of("id", id, "deleted", true));
    }

    @PostMapping("/products/{id}/price-tiers")
    public ApiResponse setPriceTiers(
            @RequestHeader(name = "X-User-Id", required = false) String userId,
            @RequestHeader(name = "X-User-Role", required = false) String role,
            @PathVariable Long id,
            @Valid @RequestBody SetTiers req) {
        List<PriceTier> tiers = req.tiers().stream().map(t -> new PriceTier(t.minQty(), t.unitPrice())).toList();
        return ApiResponse.ok(ProductView.of(service.setPriceTiers(Caller.of(userId, role), id, tiers)));
    }
}
