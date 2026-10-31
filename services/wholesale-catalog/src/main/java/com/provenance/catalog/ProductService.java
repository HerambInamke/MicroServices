package com.provenance.catalog;

import java.math.BigDecimal;
import java.math.RoundingMode;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import org.springframework.data.domain.Page;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Sort;
import org.springframework.data.jpa.domain.Specification;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

@Service
public class ProductService {

    public record ProductFields(String name, String description, Long categoryId, Integer moq, BigDecimal basePrice) {}

    public record Quote(Long productId, String wholesalerId, int qty, BigDecimal unitPrice, BigDecimal total) {}

    private final ProductRepository products;
    private final CategoryRepository categories;
    private final EventPublisher events;

    public ProductService(ProductRepository products, CategoryRepository categories, EventPublisher events) {
        this.products = products;
        this.categories = categories;
        this.events = events;
    }

    @Transactional(readOnly = true)
    public Product get(Long id) {
        return products.findById(id)
                .orElseThrow(() -> ApiException.notFound("PRODUCT_NOT_FOUND", "product not found"));
    }

    @Transactional(readOnly = true)
    public Page<Product> list(String wholesalerId, Long categoryId, int page, int limit) {
        Specification<Product> spec = Specification.where(null);
        if (wholesalerId != null) {
            spec = spec.and((root, q, cb) -> cb.equal(root.get("wholesalerId"), wholesalerId));
        }
        if (categoryId != null) {
            spec = spec.and((root, q, cb) -> cb.equal(root.get("categoryId"), categoryId));
        }
        return products.findAll(spec, PageRequest.of(page - 1, limit, Sort.by("id")));
    }

    @Transactional
    public Product create(Caller caller, ProductFields f) {
        caller.requireRole("wholesaler");
        Product p = new Product(caller.userId());
        apply(p, f);
        Product saved = products.save(p);
        events.publish("product.changed", "ProductCreated", saved.getId().toString(), EventPublisher.productPayload(saved));
        return saved;
    }

    @Transactional
    public Product update(Caller caller, Long id, ProductFields f) {
        Product p = get(id);
        caller.requireOwner(p.getWholesalerId());
        apply(p, f);
        Product saved = products.save(p);
        events.publish("product.changed", "ProductUpdated", saved.getId().toString(), EventPublisher.productPayload(saved));
        return saved;
    }

    @Transactional
    public void delete(Caller caller, Long id) {
        Product p = get(id);
        caller.requireOwner(p.getWholesalerId());
        products.delete(p);
        events.publish("product.changed", "ProductDeleted", id.toString(), Map.of("productId", id));
    }

    @Transactional
    public Product setPriceTiers(Caller caller, Long id, List<PriceTier> tiers) {
        Product p = get(id);
        caller.requireOwner(p.getWholesalerId());
        var seen = new HashSet<Integer>();
        for (PriceTier t : tiers) {
            if (!seen.add(t.getMinQty())) {
                throw ApiException.badRequest("INVALID_TIERS", "each tier needs a distinct minQty");
            }
            if (t.getMinQty() < p.getMoq()) {
                throw ApiException.badRequest("INVALID_TIERS", "tier minQty cannot be below the product MOQ");
            }
        }
        p.replacePriceTiers(tiers);
        return products.save(p);
    }

    @Transactional(readOnly = true)
    public Quote quote(Long id, int qty) {
        Product p = get(id);
        if (qty < p.getMoq()) {
            throw new ApiException(HttpStatus.UNPROCESSABLE_ENTITY, "BELOW_MOQ",
                    "minimum order quantity for this product is " + p.getMoq());
        }
        BigDecimal unit = p.unitPriceFor(qty);
        BigDecimal total = unit.multiply(BigDecimal.valueOf(qty)).setScale(2, RoundingMode.HALF_UP);
        return new Quote(p.getId(), p.getWholesalerId(), qty, unit, total);
    }

    /** Copies non-null fields onto p, so the same path serves create and partial update. */
    private void apply(Product p, ProductFields f) {
        if (f.name() != null) {
            if (f.name().isBlank()) {
                throw ApiException.badRequest("VALIDATION_ERROR", "name must not be blank");
            }
            p.setName(f.name().trim());
        }
        if (f.description() != null) {
            p.setDescription(f.description());
        }
        if (f.categoryId() != null) {
            if (!categories.existsById(f.categoryId())) {
                throw ApiException.badRequest("INVALID_CATEGORY", "category does not exist");
            }
            p.setCategoryId(f.categoryId());
        }
        if (f.moq() != null) {
            p.setMoq(f.moq());
        }
        if (f.basePrice() != null) {
            p.setBasePrice(f.basePrice());
        }
    }
}
