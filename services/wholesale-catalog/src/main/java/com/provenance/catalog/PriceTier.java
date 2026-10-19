package com.provenance.catalog;

import jakarta.persistence.Embeddable;
import java.math.BigDecimal;

/** From minQty units upward, each unit costs unitPrice. */
@Embeddable
public class PriceTier {

    private Integer minQty;

    private BigDecimal unitPrice;

    protected PriceTier() {}

    public PriceTier(Integer minQty, BigDecimal unitPrice) {
        this.minQty = minQty;
        this.unitPrice = unitPrice;
    }

    public Integer getMinQty() {
        return minQty;
    }

    public BigDecimal getUnitPrice() {
        return unitPrice;
    }
}
