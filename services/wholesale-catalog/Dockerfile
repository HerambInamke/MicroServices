FROM maven:3.9.6-eclipse-temurin-17 AS builder

WORKDIR /app
COPY pom.xml .
# Download dependencies first
RUN mvn dependency:go-offline -B

COPY src ./src
RUN mvn package -DskipTests

FROM eclipse-temurin:17-jre-alpine

WORKDIR /app
COPY --from=builder /app/target/wholesale-catalog-*.jar app.jar

ENV PORT=8083
EXPOSE 8083

CMD ["java", "-jar", "app.jar"]