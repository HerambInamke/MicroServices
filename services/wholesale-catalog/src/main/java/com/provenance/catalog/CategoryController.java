package com.provenance.catalog;

import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Size;
import java.util.List;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class CategoryController {

    public record CreateCategory(@NotBlank @Size(max = 100) String name, Long parentId) {}

    public record CategoryView(Long id, String name, Long parentId) {
        static CategoryView of(Category c) {
            return new CategoryView(c.getId(), c.getName(), c.getParentId());
        }
    }

    private final CategoryRepository categories;

    public CategoryController(CategoryRepository categories) {
        this.categories = categories;
    }

    @GetMapping("/categories")
    public ApiResponse list() {
        List<CategoryView> all = categories.findAllByOrderByNameAsc().stream().map(CategoryView::of).toList();
        return ApiResponse.ok(all);
    }

    @PostMapping("/categories")
    @ResponseStatus(HttpStatus.CREATED)
    public ApiResponse create(
            @RequestHeader(name = "X-User-Id", required = false) String userId,
            @RequestHeader(name = "X-User-Role", required = false) String role,
            @Valid @RequestBody CreateCategory req) {
        Caller.of(userId, role).requireRole("admin");
        String name = req.name().trim();
        if (req.parentId() != null && !categories.existsById(req.parentId())) {
            throw ApiException.badRequest("INVALID_CATEGORY", "parent category does not exist");
        }
        boolean taken = req.parentId() == null
                ? categories.existsByNameIgnoreCaseAndParentIdIsNull(name)
                : categories.existsByNameIgnoreCaseAndParentId(name, req.parentId());
        if (taken) {
            throw new ApiException(HttpStatus.CONFLICT, "CATEGORY_EXISTS", "a category with this name already exists here");
        }
        return ApiResponse.ok(CategoryView.of(categories.save(new Category(name, req.parentId()))));
    }
}
