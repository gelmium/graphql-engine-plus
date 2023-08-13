SET check_function_bodies = false;
CREATE TABLE public.customer (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    external_ref_list text[] NOT NULL,
    first_name text NOT NULL,
    last_name text,
    date_of_birth date,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone
);
ALTER TABLE ONLY public.customer
    ADD CONSTRAINT customer_pkey PRIMARY KEY (id);
