# Feature: supermercados-cli (dia-cli y mas-cli, fase catálogo)

**Estado:** en curso · **Creado:** 2026-10-05 · **Rama:** feat/supermercados-cli

Fase 1 de un agregador: dos binarios, uno por cadena, que buscan un producto y devuelven
**el mismo JSON**. Los carritos son fase 2 y no entran aquí.

## Specs

- **S1** — «un binario por cada supermercado.» Dos binarios independientes: `dia` y `mas`,
  cada uno con su propio paquete y su propio `cmd/`. No hay subcomando `--store`.
- **S2** — «mas es supermercadosmas.com.» El destino es `https://www.supermercadosmas.com`
  (Magento 2 + Hyvä, GraphQL en `/graphql`). No es masymas ni tienda.masymas.com.
- **S3** — «primero hacer los cli que se utilizaran por una aplicacion web que buscará un
  producto en todos los supermercados». Comando mínimo: `search <term>`. Con `--json`
  imprime el sobre `contract.Result` de `internal/contract` y **solo eso** en stdout;
  errores y logs a stderr; exit `1` en error. Los dos binarios imprimen la misma forma.
- **S4** — «luego se añadirán a carritos independientes (se evolucionará la idea mas
  adelante)». Fuera de alcance: sin login, sin carrito, sin checkout en esta feature.
- **S5** — «Si necesitas hacer REA, hazlo.» La fase de catálogo no lo requiere: las dos
  APIs están verificadas. REA queda reservado al carrito de DIA (fase 2).
- **S6** — «puedes analizar grocery-cli para reutilizar codigo.» Se reutiliza su contrato
  de datos (`internal/store/store.go`, MIT) como referencia de forma; el código se copia
  o se reescribe, nunca se importa (`internal/` no es importable entre módulos).

## Hechos verificados que condicionan la implementación

DIA (`GET https://www.dia.es/api/v1/search-back/search?q=<term>`): JSON anónimo, sin
bot-wall. Item: `object_id`, `display_name`, `brand`, `prices{price, price_per_unit,
measure_unit, strikethrough_price, is_promo_price}`, `units_in_stock`, `units_in_cart`,
`l1_category_description`, `l2_category_description`, `url`, `image`.

MAS (`POST https://www.supermercadosmas.com/graphql`): introspección abierta.
`price{regularPrice{amount{value currency}}}` funciona; **`finalPrice` no existe** y
**`price_range` devuelve HTTP 500** — no usarlos. `products(search:, pageSize:, currentPage:)`
y `total_count` verificados (245 resultados para «leche», 20 por página).

## Tareas

- [x] **T1** Andamiaje: `go.mod`, `internal/contract` (sobre JSON compartido). Inline.
- [ ] **T2** `mas-cli`: `search` contra `/graphql`, parseo de precio y €/unidad desde el
      nombre del producto. Ruta: `gentle-ai-worker` (A). Commit `feat(mas): …`
- [ ] **T3** `dia-cli`: `search` contra `search-back`, mapeo directo de `price_per_unit`
      y `measure_unit`. Ruta: `gentle-ai-worker` (B). Commit `feat(dia): …`
- [ ] **T4** Verificación independiente de ambos (`gentle-ai-verify`) + smoke real.
- [ ] **T5** `README.md` con el contrato JSON y los límites conocidos.

## Log

- **L1** (literal) «cli de supermecardos. Creo que existe un mercadona-cli. la idea es
  implementar un dia-cli, un mas-cli en base a hacer rea (skill reverse engeering anything
  skill) al de mercadona.»
- **L2** (literal) «un binario por cada supermercado. mas es supermercadosmas.com. 3 la
  idea es primero hacer los cli que se utilizaran por una aplicacion web que buscará un
  producto en todos los supermercados y luego se añadirán a carritos independientes (se
  evolucionará la idea mas adelante). Si necesitas hacer REA, hazlo. 5. No exactamente,
  pero puedes analizar grocery-cli para reutilizar codigo.»
- **L3** (literal) «continua, lanza subagentes para trabajo en paralelo»
- **Decisión:** Go (stdlib, sin dependencias), mismo contrato de salida para las dos
  cadenas, `--json` como forma canónica y tabla legible como forma por defecto.
- **Decisión:** fuera de T2/T3 todo lo que no sea `search` (product, categories, batch).
  El agregador busca un producto; lo demás se añade cuando la web lo pida.
- **Deuda asumida:** MAS necesita derivar €/kg del nombre del producto (DIA lo da hecho).
  Es la pieza frágil: exige tests de tabla propios y no puede inventar unidades.
