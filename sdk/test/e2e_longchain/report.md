# E2E 长链路评测报告

- 生成时间:2026-04-19T22:21:41+08:00
- 总任务:10
- 成功:10 (100.0%)  `target >= 65%`
- 单步通过率:100.0%
- 平均 turn 数:6.6  `target <= 10`
- 平均 token:17666  `budget threshold ~ 100k`
- 平均耗时:0s
- 平均 pattern 跨 category 切换:1.2
- 异常恢复:3/3 (100.0%)

## 任务明细

| ID | 成功 | Steps | Turns | Tokens | 切换 | 失败 Class | 失败原因 |
|---|:---:|---|---:|---:|---:|---|---|
| 01_gitea_search_star | ✓ | 3/3 | 5 | 13333 | 2 | - | - |
| 02_opencart_cart_checkout | ✓ | 6/6 | 6 | 16666 | 0 | - | - |
| 03_wordpress_post_publish | ✓ | 4/4 | 6 | 16666 | 2 | - | - |
| 04_saleor_register_buy | ✓ | 6/6 | 7 | 20000 | 1 | - | - |
| 05_adminlte_bulk_delete | ✓ | 6/6 | 6 | 15000 | 1 | - | - |
| 06_session_expired_relog | ✓ | 5/5 | 7 | 16666 | 3 | - | - |
| 07_long_multipage_form | ✓ | 10/10 | 10 | 30000 | 0 | - | - |
| 08_wishlist_similar_search | ✓ | 4/4 | 6 | 15000 | 1 | - | - |
| 09_admin_filter_export_csv | ✓ | 4/4 | 6 | 15000 | 1 | - | - |
| 10_captcha_human_handoff | ✓ | 3/3 | 7 | 18333 | 1 | - | - |

## 单步命中

| Task | Step | 期望 pattern | 命中 | 异常 | 恢复 |
|---|---|---|---|---|:---:|
| 01_gitea_search_star | login | login_username_password | login_username_password | - | - |
| 01_gitea_search_star | search_repo | search_query | search_query | - | - |
| 01_gitea_search_star | star_repo | close_modal | close_modal | - | - |
| 02_opencart_cart_checkout | browse_list | ecommerce_browse_product_list | ecommerce_browse_product_list | - | - |
| 02_opencart_cart_checkout | open_detail | ecommerce_product_detail_gallery | ecommerce_product_detail_gallery | - | - |
| 02_opencart_cart_checkout | add_cart | ecommerce_add_to_cart_with_feedback | ecommerce_add_to_cart_with_feedback | - | - |
| 02_opencart_cart_checkout | checkout | ecommerce_proceed_to_checkout | ecommerce_proceed_to_checkout | - | - |
| 02_opencart_cart_checkout | shipping | ecommerce_fill_shipping_address | ecommerce_fill_shipping_address | - | - |
| 02_opencart_cart_checkout | place_order | ecommerce_place_order_to_payment_gateway | ecommerce_place_order_to_payment_gateway | - | - |
| 03_wordpress_post_publish | login | login_username_password | login_username_password | - | - |
| 03_wordpress_post_publish | new_post | admin_row_edit | admin_row_edit | - | - |
| 03_wordpress_post_publish | save_draft | submit_generic_form | submit_generic_form | - | - |
| 03_wordpress_post_publish | publish | submit_generic_form | submit_generic_form | - | - |
| 04_saleor_register_buy | register | register_email_password | register_email_password | - | - |
| 04_saleor_register_buy | verify_email | email_verification_code | email_verification_code | - | - |
| 04_saleor_register_buy | login | login_username_password | login_username_password | - | - |
| 04_saleor_register_buy | browse | ecommerce_browse_product_list | ecommerce_browse_product_list | - | - |
| 04_saleor_register_buy | add_cart | ecommerce_add_to_cart_with_feedback | ecommerce_add_to_cart_with_feedback | - | - |
| 04_saleor_register_buy | checkout | ecommerce_proceed_to_checkout | ecommerce_proceed_to_checkout | - | - |
| 05_adminlte_bulk_delete | login | login_username_password | login_username_password | - | - |
| 05_adminlte_bulk_delete | open_users | admin_table_pagination | admin_table_pagination | - | - |
| 05_adminlte_bulk_delete | next_page | admin_table_next_page | admin_table_next_page | - | - |
| 05_adminlte_bulk_delete | filter | admin_filter_apply | admin_filter_apply | - | - |
| 05_adminlte_bulk_delete | bulk_delete | admin_batch_action | admin_batch_action | - | - |
| 05_adminlte_bulk_delete | confirm_delete | admin_row_delete_confirm | admin_row_delete_confirm | - | - |
| 06_session_expired_relog | login_initial | login_username_password | login_username_password | - | - |
| 06_session_expired_relog | browse_catalog | ecommerce_browse_product_list | ecommerce_browse_product_list | - | - |
| 06_session_expired_relog | add_cart_with_expired_session | ecommerce_add_to_cart_with_feedback | ecommerce_add_to_cart_with_feedback | session_expired | ✓ |
| 06_session_expired_relog | relog_fallback | session_expired_relog | session_expired_relog | - | - |
| 06_session_expired_relog | retry_add_cart | ecommerce_add_to_cart_with_feedback | ecommerce_add_to_cart_with_feedback | - | - |
| 07_long_multipage_form | p1_personal | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p2_address | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p3_contact | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p4_employment | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p5_income | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p6_assets | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p7_liabilities | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p8_references | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p9_disclosures | submit_generic_form | submit_generic_form | - | - |
| 07_long_multipage_form | p10_submit | submit_generic_form | submit_generic_form | - | - |
| 08_wishlist_similar_search | search | search_query | search_query | - | - |
| 08_wishlist_similar_search | open_detail | ecommerce_product_detail_gallery | ecommerce_product_detail_gallery | - | - |
| 08_wishlist_similar_search | add_wishlist | ecommerce_add_to_cart_with_feedback | ecommerce_add_to_cart_with_feedback | - | - |
| 08_wishlist_similar_search | similar | ecommerce_find_similar_product | ecommerce_find_similar_product | error_message | ✓ |
| 09_admin_filter_export_csv | login | login_username_password | login_username_password | - | - |
| 09_admin_filter_export_csv | open_orders | admin_table_pagination | admin_table_pagination | - | - |
| 09_admin_filter_export_csv | filter_last30 | admin_filter_apply | admin_filter_apply | - | - |
| 09_admin_filter_export_csv | export_csv | admin_export_csv | admin_export_csv | - | - |
| 10_captcha_human_handoff | open_login | login_username_password | login_username_password | - | - |
| 10_captcha_human_handoff | captcha_block | login_username_password | login_username_password | captcha | ✓ |
| 10_captcha_human_handoff | post_captcha_continue | close_modal | close_modal | - | - |
