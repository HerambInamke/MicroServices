package com.provenance.catalog;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.delete;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.patch;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.ArrayList;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.servlet.AutoConfigureMockMvc;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.MediaType;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.request.MockHttpServletRequestBuilder;

@SpringBootTest
@AutoConfigureMockMvc
class CatalogApiTest {

    @Autowired MockMvc mvc;
    @Autowired ObjectMapper json;
    @Autowired ProductRepository products;
    @Autowired CategoryRepository categories;
    @Autowired JdbcTemplate jdbc;
    @Autowired OutboxRelay relay;

    long categoryId;

    @BeforeEach
    void reset() throws Exception {
        jdbc.update("DELETE FROM outbox");
        products.deleteAll();
        categories.deleteAll();
        categoryId = createCategory("Sarees", null);
    }

    static MockHttpServletRequestBuilder as(MockHttpServletRequestBuilder req, String userId, String role) {
        return req.header("X-User-Id", userId).header("X-User-Role", role);
    }

    static MockHttpServletRequestBuilder body(MockHttpServletRequestBuilder req, String body) {
        return req.contentType(MediaType.APPLICATION_JSON).content(body);
    }

    long createCategory(String name, Long parentId) throws Exception {
        String parent = parentId == null ? "null" : parentId.toString();
        String res = mvc.perform(as(body(post("/categories"), "{\"name\":\"" + name + "\",\"parentId\":" + parent + "}"), "a1", "admin"))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();
        return json.readTree(res).at("/data/id").asLong();
    }

    long createProduct(String owner, int moq, String basePrice) throws Exception {
        String req = "{\"name\":\"Kanjeevaram Silk\",\"categoryId\":" + categoryId + ",\"moq\":" + moq + ",\"basePrice\":" + basePrice + "}";
        String res = mvc.perform(as(body(post("/products"), req), owner, "wholesaler"))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();
        JsonNode node = json.readTree(res);
        return node.at("/data/id").asLong();
    }

    @Test
    void categoriesAreAdminOnlyAndUnique() throws Exception {
        mvc.perform(as(body(post("/categories"), "{\"name\":\"Silk\"}"), "w1", "wholesaler"))
                .andExpect(status().isForbidden());
        mvc.perform(body(post("/categories"), "{\"name\":\"Silk\"}"))
                .andExpect(status().isUnauthorized());
        mvc.perform(as(body(post("/categories"), "{\"name\":\"sarees\"}"), "a1", "admin"))
                .andExpect(status().isConflict())
                .andExpect(jsonPath("$.error.code").value("CATEGORY_EXISTS"));
        mvc.perform(as(body(post("/categories"), "{\"name\":\"Silk\",\"parentId\":99999}"), "a1", "admin"))
                .andExpect(status().isBadRequest())
                .andExpect(jsonPath("$.error.code").value("INVALID_CATEGORY"));
        createCategory("Silk", categoryId);
        mvc.perform(get("/categories"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.data.length()").value(2))
                .andExpect(jsonPath("$.error").doesNotExist());
    }

    @Test
    void createProductValidatesRoleAndInput() throws Exception {
        String valid = "{\"name\":\"Banarasi\",\"categoryId\":" + categoryId + ",\"moq\":10,\"basePrice\":1500.00}";
        mvc.perform(as(body(post("/products"), valid), "r1", "retailer"))
                .andExpect(status().isForbidden());
        mvc.perform(body(post("/products"), valid))
                .andExpect(status().isUnauthorized());

        String[] invalid = {
            "{\"name\":\"\",\"categoryId\":" + categoryId + ",\"moq\":10,\"basePrice\":1}",
            "{\"name\":\"X\",\"categoryId\":" + categoryId + ",\"moq\":0,\"basePrice\":1}",
            "{\"name\":\"X\",\"categoryId\":" + categoryId + ",\"moq\":1,\"basePrice\":-5}",
            "{\"name\":\"X\",\"categoryId\":" + categoryId + ",\"moq\":1,\"basePrice\":1.234}",
            "{\"name\":\"X\",\"moq\":1,\"basePrice\":1}",
        };
        for (String req : invalid) {
            mvc.perform(as(body(post("/products"), req), "w1", "wholesaler"))
                    .andExpect(status().isBadRequest())
                    .andExpect(jsonPath("$.error.code").value("VALIDATION_ERROR"));
        }
        mvc.perform(as(body(post("/products"), "{\"name\":\"X\",\"categoryId\":424242,\"moq\":1,\"basePrice\":1}"), "w1", "wholesaler"))
                .andExpect(status().isBadRequest())
                .andExpect(jsonPath("$.error.code").value("INVALID_CATEGORY"));
        mvc.perform(as(body(post("/products"), "{oops"), "w1", "wholesaler"))
                .andExpect(status().isBadRequest())
                .andExpect(jsonPath("$.error.code").value("INVALID_BODY"));

        mvc.perform(as(body(post("/products"), valid), "w1", "wholesaler"))
                .andExpect(status().isCreated())
                .andExpect(jsonPath("$.data.wholesalerId").value("w1"))
                .andExpect(jsonPath("$.data.moq").value(10))
                .andExpect(jsonPath("$.data.basePrice").value(1500.00));
    }

    @Test
    void listFiltersAndPages() throws Exception {
        createProduct("w1", 1, "100");
        createProduct("w1", 1, "100");
        createProduct("w2", 1, "100");

        mvc.perform(get("/products").param("wholesalerId", "w1").param("limit", "1"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.data.length()").value(1))
                .andExpect(jsonPath("$.meta.total").value(2))
                .andExpect(jsonPath("$.meta.hasMore").value(true));
        mvc.perform(get("/products").param("categoryId", String.valueOf(categoryId)))
                .andExpect(jsonPath("$.meta.total").value(3))
                .andExpect(jsonPath("$.meta.hasMore").value(false));
        mvc.perform(get("/products").param("limit", "500"))
                .andExpect(status().isBadRequest());
        mvc.perform(get("/products").param("page", "abc"))
                .andExpect(status().isBadRequest());
        mvc.perform(get("/products/987654"))
                .andExpect(status().isNotFound())
                .andExpect(jsonPath("$.error.code").value("PRODUCT_NOT_FOUND"));
    }

    @Test
    void onlyOwnerCanUpdateDeleteOrPrice() throws Exception {
        long id = createProduct("w1", 10, "100");

        mvc.perform(as(body(patch("/products/" + id), "{\"name\":\"Stolen\"}"), "w2", "wholesaler"))
                .andExpect(status().isForbidden());
        mvc.perform(as(delete("/products/" + id), "w2", "wholesaler"))
                .andExpect(status().isForbidden());
        mvc.perform(as(body(post("/products/" + id + "/price-tiers"), "{\"tiers\":[]}"), "w2", "wholesaler"))
                .andExpect(status().isForbidden());

        mvc.perform(as(body(patch("/products/" + id), "{\"name\":\"Renamed\",\"basePrice\":120}"), "w1", "wholesaler"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.data.name").value("Renamed"))
                .andExpect(jsonPath("$.data.basePrice").value(120))
                .andExpect(jsonPath("$.data.moq").value(10));
        mvc.perform(as(body(patch("/products/" + id), "{\"name\":\"   \"}"), "w1", "wholesaler"))
                .andExpect(status().isBadRequest());

        mvc.perform(as(delete("/products/" + id), "w1", "wholesaler"))
                .andExpect(status().isOk());
        mvc.perform(get("/products/" + id))
                .andExpect(status().isNotFound());
    }

    @Test
    void priceTiersDrivePricingAndMoq() throws Exception {
        long id = createProduct("w1", 10, "100.00");
        String tiers = "{\"tiers\":[{\"minQty\":100,\"unitPrice\":80.00},{\"minQty\":50,\"unitPrice\":90.00}]}";

        mvc.perform(as(body(post("/products/" + id + "/price-tiers"), tiers), "w1", "wholesaler"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.data.priceTiers[0].minQty").value(50))
                .andExpect(jsonPath("$.data.priceTiers[1].minQty").value(100));

        String[] badTiers = {
            "{\"tiers\":[{\"minQty\":50,\"unitPrice\":90},{\"minQty\":50,\"unitPrice\":80}]}",
            "{\"tiers\":[{\"minQty\":5,\"unitPrice\":90}]}",
            "{\"tiers\":[{\"minQty\":50,\"unitPrice\":0}]}",
        };
        for (String bad : badTiers) {
            mvc.perform(as(body(post("/products/" + id + "/price-tiers"), bad), "w1", "wholesaler"))
                    .andExpect(status().isBadRequest());
        }

        mvc.perform(get("/products/" + id + "/quote").param("qty", "9"))
                .andExpect(status().isUnprocessableEntity())
                .andExpect(jsonPath("$.error.code").value("BELOW_MOQ"));
        mvc.perform(get("/products/" + id + "/quote").param("qty", "10"))
                .andExpect(jsonPath("$.data.unitPrice").value(100.00))
                .andExpect(jsonPath("$.data.total").value(1000.00));
        mvc.perform(get("/products/" + id + "/quote").param("qty", "60"))
                .andExpect(jsonPath("$.data.unitPrice").value(90.00))
                .andExpect(jsonPath("$.data.total").value(5400.00));
        mvc.perform(get("/products/" + id + "/quote").param("qty", "100"))
                .andExpect(jsonPath("$.data.unitPrice").value(80.00))
                .andExpect(jsonPath("$.data.wholesalerId").value("w1"));
        mvc.perform(get("/products/" + id + "/quote").param("qty", "0"))
                .andExpect(status().isBadRequest());

        // Replacing with an empty list clears the tiers.
        mvc.perform(as(body(post("/products/" + id + "/price-tiers"), "{\"tiers\":[]}"), "w1", "wholesaler"))
                .andExpect(jsonPath("$.data.priceTiers.length()").value(0));
    }

    /** The outbox rows, oldest first, as {topic, key, envelope}. */
    List<Object[]> outbox() {
        return jdbc.query("SELECT topic, event_key, event FROM outbox ORDER BY id",
                (rs, i) -> new Object[] {rs.getString(1), rs.getString(2), parse(rs.getString(3))});
    }

    JsonNode parse(String s) {
        try {
            return json.readTree(s);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }

    @Test
    void productChangesGoToTheOutboxOnlyOnSuccess() throws Exception {
        long id = createProduct("w1", 10, "100");
        String key = Long.toString(id);
        List<Object[]> rows = outbox();
        assertThat(rows).hasSize(1);
        assertThat(rows.get(0)[0]).isEqualTo("product.changed");
        assertThat(rows.get(0)[1]).isEqualTo(key);
        JsonNode created = (JsonNode) rows.get(0)[2];
        assertThat(created.get("eventType").asText()).isEqualTo("ProductCreated");
        assertThat(created.get("sourceService").asText()).isEqualTo("wholesale-catalog");
        assertThat(created.hasNonNull("eventId") && created.hasNonNull("timestamp")).isTrue();
        assertThat(created.at("/payload/productId").asLong()).isEqualTo(id);
        assertThat(created.at("/payload/wholesalerId").asText()).isEqualTo("w1");

        mvc.perform(as(body(patch("/products/" + id), "{\"name\":\"Stolen\"}"), "w2", "wholesaler"))
                .andExpect(status().isForbidden());
        mvc.perform(as(body(post("/products"), "{\"name\":\"\"}"), "w1", "wholesaler"))
                .andExpect(status().isBadRequest());
        mvc.perform(as(body(patch("/products/" + id), "{\"categoryId\":999999}"), "w1", "wholesaler"))
                .andExpect(status().isBadRequest());
        assertThat(outbox()).hasSize(1);

        mvc.perform(as(body(patch("/products/" + id), "{\"name\":\"Renamed\"}"), "w1", "wholesaler"))
                .andExpect(status().isOk());
        mvc.perform(as(delete("/products/" + id), "w1", "wholesaler"))
                .andExpect(status().isOk());
        rows = outbox();
        // One topic keyed by product id, so Kafka keeps a product's create, update and delete in order.
        assertThat(rows).extracting(r -> r[0]).containsOnly("product.changed");
        assertThat(rows).extracting(r -> r[1]).containsOnly(key);
        assertThat(rows).extracting(r -> ((JsonNode) r[2]).get("eventType").asText())
                .containsExactly("ProductCreated", "ProductUpdated", "ProductDeleted");
        assertThat(((JsonNode) rows.get(1)[2]).at("/payload/name").asText()).isEqualTo("Renamed");
        assertThat(((JsonNode) rows.get(2)[2]).get("payload").toString()).isEqualTo("{\"productId\":" + id + "}");
    }

    @Test
    void relaySendsInOrderAndKeepsEventsWhileKafkaIsDown() throws Exception {
        createProduct("w1", 10, "100");
        createProduct("w1", 10, "200");
        List<Object[]> written = outbox();

        List<String> attempts = new ArrayList<>();
        assertThat(relay.relayOnce((topic, key, value) -> attempts.add(key) && false)).isZero();
        assertThat(attempts).containsExactly((String) written.get(0)[1]); // the second never overtakes the first
        assertThat(outbox()).hasSize(2);

        List<String> sent = new ArrayList<>();
        assertThat(relay.relayOnce((topic, key, value) -> sent.add(value))).isEqualTo(2);
        assertThat(sent).extracting(this::parse).containsExactly((JsonNode) written.get(0)[2], (JsonNode) written.get(1)[2]); // same eventIds
        assertThat(outbox()).isEmpty();
    }
}
