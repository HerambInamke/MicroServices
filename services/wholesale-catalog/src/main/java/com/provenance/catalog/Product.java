package com.provenance.catalog;

import jakarta.persistence.CollectionTable;
import jakarta.persistence.ElementCollection;
import jakarta.persistence.Entity;
import jakarta.persistence.FetchType;
import jakarta.persistence.GeneratedValue;
import jakarta.persistence.GenerationType;
import jakarta.persistence.Id;
import jakarta.persistence.JoinColumn;
import jakarta.persistence.OrderBy;
import jakarta.persistence.PrePersist;
import jakarta.persistence.PreUpdate;
import jakarta.persistence.Table;
import java.math.BigDecimal;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;

@Entity
@Table(name = "products")
public class Product {

    @Id
    @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;

    private String wholesalerId;
    private String name;
    private String description;
    private Long categoryId;
    private Integer moq;
    private BigDecimal basePrice;
    private Instant createdAt;
    private Instant updatedAt;

    // Eager: tiers are a handful of rows and are always shown with the product.
    @ElementCollection(fetch = FetchType.EAGER)
    @CollectionTable(name = "price_tiers", joinColumns = @JoinColumn(name = "product_id"))
    @OrderBy("minQty ASC")
    private List<PriceTier> priceTiers = new ArrayList<>();

    protected Product() {}

    public Product(String wholesalerId) {
        this.wholesalerId = wholesalerId;
    }

    @PrePersist
    void onCreate() {
        createdAt = updatedAt = Instant.now();
    }

    @PreUpdate
    void onUpdate() {
        updatedAt = Instant.now();
    }

    /** Unit price for qty: the highest tier whose minQty is reached, else the base price. */
    public BigDecimal unitPriceFor(int qty) {
        BigDecimal price = basePrice;
        int best = 0;
        for (PriceTier t : priceTiers) {
            if (t.getMinQty() <= qty && t.getMinQty() > best) {
                best = t.getMinQty();
                price = t.getUnitPrice();
            }
        }
        return price;
    }

    public Long getId() {
        return id;
    }

    public String getWholesalerId() {
        return wholesalerId;
    }

    public String getName() {
        return name;
    }

    public void setName(String name) {
        this.name = name;
    }

    public String getDescription() {
        return description;
    }

    public void setDescription(String description) {
        this.description = description;
    }

    public Long getCategoryId() {
        return categoryId;
    }

    public void setCategoryId(Long categoryId) {
        this.categoryId = categoryId;
    }

    public Integer getMoq() {
        return moq;
    }

    public void setMoq(Integer moq) {
        this.moq = moq;
    }

    public BigDecimal getBasePrice() {
        return basePrice;
    }

    public void setBasePrice(BigDecimal basePrice) {
        this.basePrice = basePrice;
    }

    public Instant getCreatedAt() {
        return createdAt;
    }

    public Instant getUpdatedAt() {
        return updatedAt;
    }

    public List<PriceTier> getPriceTiers() {
        return priceTiers;
    }

    public void replacePriceTiers(List<PriceTier> tiers) {
        priceTiers.clear();
        priceTiers.addAll(tiers);
        priceTiers.sort(Comparator.comparing(PriceTier::getMinQty));
    }
}
